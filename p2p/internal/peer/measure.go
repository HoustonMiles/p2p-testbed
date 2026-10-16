package peer

import (
	"math"
	"p2ptestbed/p2p/internal/noise"
	"p2ptestbed/p2p/internal/wire"
	"sort"
	"time"
)

// TODO(paper §5 Measures #4): record CPU usage per peer during throughput,
// and compare with iperf3 as the paper's reference tool.
//
// TODO(paper §5 Measures #5-6): goodput under attack and recovery time need
// goodput sampled over time (e.g. every 100 ms), not one total per run.

const ipUDPHeader = 28

// measure runs steps 4 and 5.
func (r *runner) measure() error {
	if r.cfg.Pings > 0 {
		r.pingPhase()
		if err := r.barrier(stepPinged, 0, r.cfg.StepTimeout); err != nil {
			return err
		}
	}
	if r.cfg.RateMbit <= 0 {
		return nil
	}
	first := r.id < r.peerID.Load()
	if err := r.throughput(stepFirstSend, first); err != nil {
		return err
	}
	return r.throughput(stepSecondSend, !first)
}

func (r *runner) pingPhase() {
	sent := 0
	r.pump(time.Duration(r.cfg.Pings+1)*r.cfg.PingInterval+r.cfg.StepTimeout, r.cfg.PingInterval,
		func() {
			if sent < r.cfg.Pings {
				r.sendPeer(wire.Ping, uint64(sent), wire.U64(uint64(time.Since(r.t0))))
				sent++
			}
		},
		func() bool { return sent >= r.cfg.Pings })
	r.pump(r.cfg.PingGrace, 0, nil, func() bool { return len(r.rtts) >= r.cfg.Pings })
	r.recordPings(sent)
}

func (r *runner) recordPings(sent int) {
	r.res.PingsSent, r.res.PingsRecv = sent, len(r.rtts)
	r.res.PingLoss = ptr(1 - float64(len(r.rtts))/float64(sent))
	r.res.RTTs = make([]float64, 0, len(r.rtts))
	for seq := uint64(0); seq < uint64(sent); seq++ {
		if v, ok := r.rtts[seq]; ok {
			r.res.RTTs = append(r.res.RTTs, v)
		}
	}
	if len(r.res.RTTs) == 0 {
		return
	}
	setRTTStats(r.res, r.res.RTTs)
	r.logf("ping: %d/%d replies, median %.2f ms", len(r.res.RTTs), sent, *r.res.RTTP50)
}

func setRTTStats(res *Result, samples []float64) {
	s := append([]float64(nil), samples...)
	sort.Float64s(s)
	mean := 0.0
	for _, v := range s {
		mean += v
	}
	mean /= float64(len(s))
	res.RTTMean, res.RTTMin, res.RTTMax = ptr(mean), ptr(s[0]), ptr(s[len(s)-1])
	res.RTTP50, res.RTTP95 = ptr(percentile(s, 50)), ptr(percentile(s, 95))
	if len(s) > 1 {
		v := 0.0
		for _, x := range s {
			v += (x - mean) * (x - mean)
		}
		res.RTTStdev = ptr(math.Sqrt(v / float64(len(s)-1)))
	}
}

func (r *runner) onPing(m inbound) { r.reply(m, wire.Pong, m.pkt.Seq, m.pkt.Body) }

func (r *runner) onPong(m inbound) {
	sentAt := time.Duration(wire.ParseU64(m.pkt.Body))
	if _, dup := r.rtts[m.pkt.Seq]; !dup && m.pkt.Seq < uint64(r.cfg.Pings) {
		r.rtts[m.pkt.Seq] = ms(m.at - sentAt)
	}
}

// throughput runs one direction. The receiver waits at the barrier while
// the sender sends; the barrier value carries how many packets were sent.
func (r *runner) throughput(step uint64, weSend bool) error {
	var sent uint64
	if weSend {
		sent = r.sendData()
	}
	if err := r.barrier(step, sent, r.cfg.Duration+r.cfg.StepTimeout); err != nil {
		return err
	}
	r.pump(r.cfg.Drain, 0, nil, never)
	if !weSend {
		r.recordRx(int64(r.peerValue[step]))
	}
	return nil
}

// sendData paces Size-byte Data packets against the clock every
// millisecond, so the average rate stays exact.
func (r *runner) sendData() uint64 {
	size := max(r.cfg.Size, wire.HeaderLen)
	pps := r.cfg.RateMbit * 1e6 / float64(size*8)
	pkt := wire.Packet{Type: wire.Data, Room: r.cfg.Room, From: r.id,
		Body: make([]byte, size-wire.HeaderLen)}.Append(nil)
	var sent uint64
	start := time.Now()
	r.pump(r.cfg.Duration+time.Second, time.Millisecond,
		func() {
			due := uint64(math.Min(time.Since(start).Seconds(), r.cfg.Duration.Seconds()) * pps)
			for ; sent < due; sent++ {
				wire.PutSeq(pkt, sent)
				r.sendDataPacket(pkt)
			}
		},
		func() bool { return time.Since(start) >= r.cfg.Duration })
	elapsed := time.Since(start).Seconds()
	r.res.TxPackets = ptr(int64(sent))
	r.res.TxMbit = ptr(float64(sent) * float64(size) * 8 / elapsed / 1e6)
	r.logf("throughput: sent %d packets (%.2f Mbit/s)", sent, *r.res.TxMbit)
	return sent
}

// sendDataPacket sends one Data packet; errors count as loss.
func (r *runner) sendDataPacket(pkt []byte) {
	if r.hopper != nil {
		r.hopper.Send(pkt)
	} else {
		r.conn.WriteToUDPAddrPort(pkt, r.remote)
	}
}

type rxCounts struct {
	packets, bytes int64
	first, last    time.Duration
}

func (r *runner) countData(at time.Duration, n int) {
	r.rxMu.Lock()
	defer r.rxMu.Unlock()
	if r.rx.packets == 0 {
		r.rx.first = at
	}
	r.rx.packets++
	r.rx.bytes += int64(n)
	r.rx.last = at
}

func (r *runner) recordRx(peerSent int64) {
	r.rxMu.Lock()
	rx := r.rx
	r.rxMu.Unlock()
	r.res.RxPackets, r.res.RxPeerSent = ptr(rx.packets), ptr(peerSent)
	if peerSent > 0 {
		r.res.RxLoss = ptr(1 - float64(rx.packets)/float64(peerSent))
	}
	window := (rx.last - rx.first).Seconds()
	if rx.packets < 2 || window <= 0 {
		return
	}
	// n packets span n-1 gaps, so count n-1 packets' worth.
	frac := float64(rx.packets-1) / float64(rx.packets)
	r.res.RxMbit = ptr(float64(rx.bytes) * frac * 8 / window / 1e6)
	r.res.RxIPMbit = ptr(float64(rx.bytes+rx.packets*r.headerBytes()) * frac * 8 / window / 1e6)
	r.logf("throughput: received %d/%d packets, %.2f Mbit/s", rx.packets, peerSent, *r.res.RxMbit)
}

func (r *runner) headerBytes() int64 {
	if r.hopper != nil {
		return ipUDPHeader + noise.Overhead
	}
	return ipUDPHeader
}

func percentile(sorted []float64, p float64) float64 {
	k := float64(len(sorted)-1) * p / 100
	lo := int(k)
	hi := min(lo+1, len(sorted)-1)
	return sorted[lo] + (sorted[hi]-sorted[lo])*(k-float64(lo))
}
