package peer

import (
	"math"
	"sort"
	"time"

	"p2ptestbed/p2p/internal/noise"
	"p2ptestbed/p2p/internal/wire"
)

// TODO(paper §5 Measures #4): record CPU usage per peer during throughput,
// and compare with iperf3 as the paper's reference tool.
//
// TODO(paper §5 Measures #5-6): goodput under attack and recovery time need
// goodput sampled over time (e.g. every 100 ms), not one total per run.

const ipUDPHeader = 28

// measure runs steps 4 and 5: pings on every link at once, then the
// throughput tests one pair at a time.
func (r *runner) measure() {
	if r.cfg.Pings > 0 {
		r.pingAll()
		r.barrierAll(barrierWait{step: stepPinged, timeout: r.stepTimeout()})
	}
	for _, l := range r.testOrder() {
		if r.cfg.RateMbit > 0 {
			l.throughputTest()
		}
		l.finish()
	}
}

func (r *runner) pingAll() {
	ls, sent := r.active(), 0
	r.pump(time.Duration(r.cfg.Pings+1)*r.cfg.PingInterval+r.cfg.StepTimeout, r.cfg.PingInterval,
		func() {
			if sent < r.cfg.Pings {
				for _, l := range ls {
					l.send(wire.Ping, uint64(sent), wire.U64(uint64(time.Since(r.t0))))
				}
				sent++
			}
		},
		func() bool { return sent >= r.cfg.Pings })
	r.pump(r.cfg.PingGrace, 0, nil, func() bool {
		return allLinks(ls, func(l *link) bool { return len(l.rtts) >= r.cfg.Pings })
	})
	for _, l := range ls {
		l.recordPings(sent)
	}
}

func (l *link) recordPings(sent int) {
	res := &l.res
	res.PingsSent, res.PingsRecv = sent, len(l.rtts)
	res.PingLoss = ptr(1 - float64(len(l.rtts))/float64(sent))
	res.RTTs = make([]float64, 0, len(l.rtts))
	for seq := uint64(0); seq < uint64(sent); seq++ {
		if v, ok := l.rtts[seq]; ok {
			res.RTTs = append(res.RTTs, v)
		}
	}
	if len(res.RTTs) == 0 {
		return
	}
	setRTTStats(res, res.RTTs)
	l.logf("ping: %d/%d replies, median %.2f ms", len(res.RTTs), sent, *res.RTTP50)
}

func setRTTStats(res *LinkResult, samples []float64) {
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

func (l *link) onPing(m inbound) { l.reply(m, wire.Pong, m.pkt.Seq, m.pkt.Body) }

func (l *link) onPong(m inbound) {
	sentAt := time.Duration(wire.ParseU64(m.pkt.Body))
	if _, dup := l.rtts[m.pkt.Seq]; !dup && m.pkt.Seq < uint64(l.r.cfg.Pings) {
		l.rtts[m.pkt.Seq] = ms(m.at - sentAt)
	}
}

// throughputTest runs both directions on this link once the member is at
// this pair too; it may first be testing pairs earlier in the order, which
// the wait allows for. Nothing is sent before both sides are ready, so no
// two tests share a peer's link capacity.
func (l *link) throughputTest() {
	r := l.r
	testTime := 2*(r.cfg.Duration+r.cfg.Drain) + 2*time.Second
	ready := barrierWait{step: stepTestReady, timeout: r.stepTimeout() + time.Duration(l.pairsAhead())*testTime}
	if missed := r.barrier([]*link{l}, ready); len(missed) > 0 {
		l.fail(ready.errFor(l))
		return
	}
	first := r.id < l.peerID
	if l.throughput(stepFirstSend, first) {
		l.throughput(stepSecondSend, !first)
	}
}

// throughput runs one direction. The receiver waits at the barrier while
// the sender sends; the barrier value carries how many packets were sent.
func (l *link) throughput(step uint64, weSend bool) bool {
	r := l.r
	var sent uint64
	if weSend {
		sent = l.sendData()
	}
	w := barrierWait{step: step, value: sent, timeout: r.cfg.Duration + r.cfg.StepTimeout}
	if missed := r.barrier([]*link{l}, w); len(missed) > 0 {
		l.fail(w.errFor(l))
		return false
	}
	r.pump(r.cfg.Drain, 0, nil, never)
	if !weSend {
		l.recordRx(int64(l.peerValue[step]))
	}
	return true
}

// sendData paces Size-byte Data packets against the clock every
// millisecond, so the average rate stays exact.
func (l *link) sendData() uint64 {
	r := l.r
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
				l.sendDataPacket(pkt)
			}
		},
		func() bool { return time.Since(start) >= r.cfg.Duration })
	elapsed := time.Since(start).Seconds()
	l.res.TxPackets = ptr(int64(sent))
	l.res.TxMbit = ptr(float64(sent) * float64(size) * 8 / elapsed / 1e6)
	l.logf("throughput: sent %d packets (%.2f Mbit/s)", sent, *l.res.TxMbit)
	return sent
}

// sendDataPacket sends one Data packet; errors count as loss.
func (l *link) sendDataPacket(pkt []byte) {
	if l.hopper != nil {
		l.hopper.Send(pkt)
	} else {
		l.r.conn.WriteToUDPAddrPort(pkt, l.remote)
	}
}

type rxCounts struct {
	packets, bytes int64
	first, last    time.Duration
}

func (l *link) countData(at time.Duration, n int) {
	l.rxMu.Lock()
	defer l.rxMu.Unlock()
	if l.rx.packets == 0 {
		l.rx.first = at
	}
	l.rx.packets++
	l.rx.bytes += int64(n)
	l.rx.last = at
}

func (l *link) recordRx(peerSent int64) {
	l.rxMu.Lock()
	rx := l.rx
	l.rxMu.Unlock()
	res := &l.res
	res.RxPackets, res.RxPeerSent = ptr(rx.packets), ptr(peerSent)
	if peerSent > 0 {
		res.RxLoss = ptr(1 - float64(rx.packets)/float64(peerSent))
	}
	if rx.packets > 0 {
		res.RxStartMs, res.RxEndMs = ptr(ms(rx.first)), ptr(ms(rx.last))
	}
	window := (rx.last - rx.first).Seconds()
	if rx.packets < 2 || window <= 0 {
		return
	}
	// n packets span n-1 gaps, so count n-1 packets' worth.
	frac := float64(rx.packets-1) / float64(rx.packets)
	res.RxMbit = ptr(float64(rx.bytes) * frac * 8 / window / 1e6)
	res.RxIPMbit = ptr(float64(rx.bytes+rx.packets*l.headerBytes()) * frac * 8 / window / 1e6)
	l.logf("throughput: received %d/%d packets, %.2f Mbit/s", rx.packets, peerSent, *res.RxMbit)
}

func (l *link) headerBytes() int64 {
	if l.hopper != nil {
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
