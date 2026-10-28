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

const ipUDPHeader = 28

// ping runs step 4 on the link, then meets the member at a barrier.
func (l *link) ping() {
	c, sent := l.r.cfg, 0
	l.pump(time.Duration(c.Pings+1)*c.PingInterval+c.StepTimeout, c.PingInterval,
		func() {
			if sent < c.Pings {
				l.send(wire.Ping, uint64(sent), wire.U64(uint64(time.Since(l.r.t0))))
				sent++
			}
		},
		func() bool { return sent >= c.Pings })
	l.pump(c.PingGrace, 0, nil, func() bool { return len(l.rtts) >= c.Pings })
	l.recordPings(sent)
	l.barrier(barrierWait{step: stepPinged, timeout: c.StepTimeout})
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
// this pair too; it may first be bringing up its links and testing pairs
// earlier in the order, which the wait allows for. Nothing is sent before
// both sides are ready, so no two tests share a peer's link capacity.
func (l *link) throughputTest() {
	r := l.r
	testTime := 2*(r.cfg.Duration+r.cfg.Drain) + 2*time.Second
	ready := barrierWait{step: stepTestReady, timeout: r.cfg.StepTimeout + r.setupTime() + time.Duration(l.pairsAhead())*testTime}
	if !l.barrier(ready) {
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
	if !l.barrier(barrierWait{step: step, value: sent, timeout: r.cfg.Duration + r.cfg.StepTimeout}) {
		return false
	}
	l.pump(r.cfg.Drain, 0, nil, never)
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
	tx := series{every: r.cfg.SampleInterval}
	start := time.Now()
	l.pump(r.cfg.Duration+time.Second, time.Millisecond,
		func() {
			due := uint64(math.Min(time.Since(start).Seconds(), r.cfg.Duration.Seconds()) * pps)
			at := time.Since(r.t0)
			for ; sent < due; sent++ {
				wire.PutSeq(pkt, sent)
				l.sendDataPacket(pkt)
				tx.add(at, len(pkt))
			}
		},
		func() bool { return time.Since(start) >= r.cfg.Duration })
	elapsed := time.Since(start).Seconds()
	l.res.TxPackets = ptr(int64(sent))
	l.res.TxMbit = ptr(float64(sent) * float64(size) * 8 / elapsed / 1e6)
	l.res.TxSeriesStartMs, l.res.TxSeriesMbit = tx.result()
	l.logf("throughput: sent %d packets (%.2f Mbit/s)", sent, *l.res.TxMbit)
	return sent
}

// sendDataPacket sends one Data packet; errors count as loss.
func (l *link) sendDataPacket(pkt []byte) {
	switch {
	case l.hopper != nil:
		l.hopper.Send(pkt)
	case l.r.relay != nil:
		l.transmit(l.seal(pkt))
	default:
		l.r.conn.WriteToUDPAddrPort(pkt, l.remote)
	}
}

type rxCounts struct {
	packets, bytes int64
	first, last    time.Duration
	series         series
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
	l.rx.series.add(at, n)
}

func (l *link) recordRx(peerSent int64) {
	l.rxMu.Lock()
	rx := l.rx
	l.res.RxSeriesStartMs, l.res.RxSeriesMbit = rx.series.result() // late packets may still be counting
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

// headerBytes is what each Data packet carries on the wire besides its
// payload: IP and UDP, plus Noise's header and tag when sealed, plus
// ChannelData's 4 bytes when it came through the relay.
func (l *link) headerBytes() int64 {
	n := int64(ipUDPHeader)
	if l.hopper != nil || l.sealed.Load() != nil {
		n += noise.Overhead
	}
	if l.r.relay != nil {
		n += channelDataHeader
	}
	return n
}

const channelDataHeader = 4

// series is a Data rate over time: bytes counted in fixed intervals of the
// run's clock (time since start), so each member's series lines up with
// the others' through start_unix_ms. Intervals start at multiples of
// every, so the first and last are usually partly filled; one with
// nothing in it between two that have data reads 0.
type series struct {
	every time.Duration // 0: off
	first int64         // the interval bins[0] counts, in multiples of every
	bins  []int64       // bytes
}

func (s *series) add(at time.Duration, n int) {
	if s.every <= 0 {
		return
	}
	i := int64(at / s.every)
	switch {
	case s.bins == nil:
		s.first = i
	case i < s.first: // reader goroutines can count a little out of order
		s.bins = append(make([]int64, s.first-i), s.bins...)
		s.first = i
	}
	for int64(len(s.bins)) <= i-s.first {
		s.bins = append(s.bins, 0)
	}
	s.bins[i-s.first] += int64(n)
}

// result is when the series starts, since start, and its rate in Mbit/s
// per interval; both nil when it is off or nothing was counted.
func (s *series) result() (*float64, []float64) {
	if s.bins == nil {
		return nil, nil
	}
	mbit := make([]float64, len(s.bins))
	for i, b := range s.bins {
		mbit[i] = math.Round(float64(b)*8/s.every.Seconds()/1e3) / 1e3 // to the kbit/s
	}
	return ptr(ms(time.Duration(s.first) * s.every)), mbit
}

func percentile(sorted []float64, p float64) float64 {
	k := float64(len(sorted)-1) * p / 100
	lo := int(k)
	hi := min(lo+1, len(sorted)-1)
	return sorted[lo] + (sorted[hi]-sorted[lo])*(k-float64(lo))
}
