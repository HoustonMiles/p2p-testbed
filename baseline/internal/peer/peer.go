// Package peer is one side of the raw UDP baseline: STUN, rendezvous, hole
// punching, then peer-to-peer RTT and throughput over the punched path.
// No encryption and no port-hopping, so these numbers are the floor the
// direct channel (Noise + hopping) is compared against.
//
// Everything uses ONE UDP socket, because the NAT mapping that the
// rendezvous server sees is the one the peers punch through.
//
// Steps, run by both peers at the same time:
//
//  1. STUN       ask the STUN server for our public address (reported only)
//  2. Rendezvous register with the rendezvous server until it returns the
//     other peer's public address
//  3. Punch      send Punch to that address every PunchInterval until a
//     PunchAck comes back, which proves the path works both ways.
//     A Punch that arrives from a different address than the
//     rendezvous gave us (the other side is behind an endpoint-
//     dependent NAT) is the real one, so we switch to it.
//  4. Ping       both peers ping each other at the same time
//  5. Throughput a sends to b at RateMbit for Duration, then b sends to a;
//     each receiver reports what arrived
//
// Between steps both peers meet at a barrier, so one side's throughput test
// never overlaps the other side's ping test.
package peer

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"p2ptestbed/baseline/internal/stun"
	"p2ptestbed/baseline/internal/wire"
)

type Config struct {
	Role       byte   // 'a' or 'b'
	Session    uint64 // shared by both peers of one trial
	Local      string // local UDP address to bind, e.g. ":0"
	STUN       string // STUN server host:port; "" skips step 1
	Rendezvous string // rendezvous server host:port

	STUNTimeout       time.Duration
	RendezvousTimeout time.Duration
	PunchTimeout      time.Duration
	PunchInterval     time.Duration
	StepTimeout       time.Duration // how long to wait for the other peer at a barrier

	Pings        int // 0 skips step 4
	PingInterval time.Duration
	PingGrace    time.Duration // wait this long for late replies after the last ping

	RateMbit float64       // offered load per direction; 0 skips step 5
	Duration time.Duration // per direction
	Size     int           // UDP payload bytes per Data packet
	Drain    time.Duration // after a direction ends, wait for queued packets

	Linger time.Duration // keep answering the peer this long before exiting

	Logf func(format string, args ...any)
}

// Result is the tool's JSON output. Pointers are null when a step was
// skipped or did not happen, so every trial has the same columns.
type Result struct {
	Role    string `json:"role"`
	Session uint64 `json:"session"`
	Local   string `json:"local"`
	Error   string `json:"error"`

	// Addresses. stun_addr vs rdv_addr: the same socket's public address as
	// seen by two different servers. They differ behind an endpoint-dependent
	// ("symmetric") NAT.
	STUNAddr        *string `json:"stun_addr"`
	RdvAddr         *string `json:"rdv_addr"`
	MappingVaries   *bool   `json:"mapping_varies"`
	PeerRdvAddr     *string `json:"peer_rdv_addr"`     // the other peer, as the rendezvous saw it
	PeerAddr        *string `json:"peer_addr"`         // the address that actually worked
	PeerAddrChanged *bool   `json:"peer_addr_changed"` // learned from its packets (peer-reflexive)

	// Timing (ms since the tool started, or durations)
	STUNMs       *float64 `json:"stun_ms"`
	RendezvousMs *float64 `json:"rendezvous_ms"` // until the rendezvous introduced the peer
	Punched      bool     `json:"punched"`
	PunchMs      *float64 `json:"punch_ms"` // from the introduction to the first PunchAck
	PunchesSent  int      `json:"punches_sent"`
	PunchesRecv  int      `json:"punches_recv"`

	// Peer-to-peer RTT, measured by this side
	PingsSent int       `json:"pings_sent"`
	PingsRecv int       `json:"pings_recv"`
	PingLoss  *float64  `json:"ping_loss"`
	RTTMean   *float64  `json:"rtt_ms_mean"`
	RTTP50    *float64  `json:"rtt_ms_p50"`
	RTTP95    *float64  `json:"rtt_ms_p95"`
	RTTMin    *float64  `json:"rtt_ms_min"`
	RTTMax    *float64  `json:"rtt_ms_max"`
	RTTStdev  *float64  `json:"rtt_ms_stdev"`
	RTTs      []float64 `json:"rtt_ms_samples"` // in ping order; lost pings omitted

	// Throughput. tx_* is what this side sent; rx_* is what arrived here
	// from the other side (so a's rx_* is the b->a direction).
	RateMbit    float64  `json:"rate_mbit"`
	PacketBytes int      `json:"packet_bytes"`
	TxPackets   *int64   `json:"tx_packets"`
	TxMbit      *float64 `json:"tx_mbit"` // achieved sending rate (UDP payload)
	RxPackets   *int64   `json:"rx_packets"`
	RxPeerSent  *int64   `json:"rx_peer_sent"`
	RxLoss      *float64 `json:"rx_loss"`
	RxMbit      *float64 `json:"rx_mbit"`    // UDP payload rate while packets arrived
	RxIPMbit    *float64 `json:"rx_ip_mbit"` // same, counting the 28-byte IPv4+UDP headers

	TotalMs float64 `json:"total_ms"`
}

// Barrier steps.
const (
	stepConnected = 1
	stepPinged    = 2
	stepAtoB      = 3
	stepBtoA      = 4
)

const ipUDPHeader = 28

type inbound struct {
	from netip.AddrPort
	at   time.Duration // since start, taken when the packet was read
	pkt  wire.Packet
	stun []byte // set instead of pkt for STUN messages
}

type runner struct {
	cfg  Config
	conn *net.UDPConn
	in   chan inbound
	t0   time.Time
	res  *Result

	stunTx  stun.TxID
	stunGot netip.AddrPort

	rdv       netip.AddrPort
	peerInfo  bool
	peerRdv   netip.AddrPort
	remote    netip.AddrPort // where peer traffic goes; updated by learn()
	gotAck    bool
	ackAt     time.Duration
	reached   uint64            // highest barrier step this side has reached
	myValue   map[uint64]uint64 // value we attached to each step
	peerValue map[uint64]uint64 // steps the peer has reached, with its value
	rtts      map[uint64]float64

	// Data counters, updated by the reader goroutine.
	rxMu      sync.Mutex
	rxPackets int64
	rxBytes   int64
	rxFirst   time.Duration
	rxLast    time.Duration
}

// Run performs every step and returns what it measured. A failed punch is a
// result (Punched=false), not an error; errors mean the run itself broke.
func Run(cfg Config) (*Result, error) {
	r := &runner{
		cfg:       cfg,
		t0:        time.Now(),
		in:        make(chan inbound, 4096),
		myValue:   map[uint64]uint64{},
		peerValue: map[uint64]uint64{},
		rtts:      map[uint64]float64{},
		res: &Result{Role: string(cfg.Role), Session: cfg.Session,
			RateMbit: cfg.RateMbit, PacketBytes: cfg.Size},
	}
	err := r.run()
	r.res.TotalMs = ms(time.Since(r.t0))
	if err != nil {
		r.res.Error = err.Error()
	}
	return r.res, err
}

func (r *runner) run() error {
	if r.cfg.Role != 'a' && r.cfg.Role != 'b' {
		return errors.New("role must be a or b")
	}
	laddr, err := net.ResolveUDPAddr("udp4", r.cfg.Local)
	if err != nil {
		return err
	}
	r.conn, err = net.ListenUDP("udp4", laddr)
	if err != nil {
		return err
	}
	defer r.conn.Close()
	r.conn.SetReadBuffer(4 << 20)
	r.conn.SetWriteBuffer(4 << 20)
	r.res.Local = r.conn.LocalAddr().String()
	go r.readLoop()

	if r.rdv, err = resolve(r.cfg.Rendezvous); err != nil {
		return fmt.Errorf("rendezvous address: %w", err)
	}

	// 1. STUN
	if r.cfg.STUN != "" {
		if err := r.doSTUN(); err != nil {
			r.logf("stun: %v (continuing without it)", err)
		}
	}

	// 2. Rendezvous
	ok := r.pump(r.cfg.RendezvousTimeout, 100*time.Millisecond,
		func() { r.sendTo(r.rdv, wire.Register, 0, nil) },
		func() bool { return r.peerInfo })
	if !ok {
		return errors.New("no introduction from the rendezvous server (is the other peer running?)")
	}

	// 3. Punch
	introducedAt := time.Since(r.t0)
	r.pump(r.cfg.PunchTimeout, r.cfg.PunchInterval,
		func() { r.res.PunchesSent++; r.sendTo(r.remote, wire.Punch, uint64(r.res.PunchesSent), nil) },
		func() bool { return r.gotAck })
	r.res.PeerAddr = ptr(r.remote.String())
	r.res.PeerAddrChanged = ptr(r.remote != r.peerRdv)
	if !r.gotAck {
		r.logf("punch: no reply from %v after %v", r.remote, r.cfg.PunchTimeout)
		return nil
	}
	r.res.Punched = true
	r.res.PunchMs = ptr(ms(r.ackAt - introducedAt))
	r.logf("punch: connected to %v in %.1f ms", r.remote, *r.res.PunchMs)
	if err := r.barrier(stepConnected, 0, r.cfg.StepTimeout); err != nil {
		return err
	}

	// 4. Ping
	if r.cfg.Pings > 0 {
		r.pingPhase()
		if err := r.barrier(stepPinged, 0, r.cfg.StepTimeout); err != nil {
			return err
		}
	}

	// 5. Throughput, a -> b then b -> a
	if r.cfg.RateMbit > 0 {
		for _, dir := range []struct {
			step   uint64
			sender byte
		}{{stepAtoB, 'a'}, {stepBtoA, 'b'}} {
			var sent uint64
			if dir.sender == r.cfg.Role {
				sent = r.sendData()
			}
			// The receiver waits here while the sender sends.
			if err := r.barrier(dir.step, sent, r.cfg.Duration+r.cfg.StepTimeout); err != nil {
				return err
			}
			r.pump(r.cfg.Drain, 0, nil, never)
			if dir.sender != r.cfg.Role {
				r.recordRx(int64(r.peerValue[dir.step]))
			}
		}
	}

	r.pump(r.cfg.Linger, 0, nil, never)
	return nil
}

// ---------------------------------------------------------------------------
// Event loop
// ---------------------------------------------------------------------------

// readLoop runs on its own goroutine. Data packets are only counted here (they
// can arrive at line rate); everything else goes to the main loop.
func (r *runner) readLoop() {
	defer close(r.in)
	buf := make([]byte, 65536)
	for {
		n, from, err := r.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		at := time.Since(r.t0)
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		b := buf[:n]
		if stun.IsMessage(b) {
			r.in <- inbound{from: from, at: at, stun: append([]byte(nil), b...)}
			continue
		}
		p, ok := wire.Parse(b)
		if !ok || p.Session != r.cfg.Session || p.Role == r.cfg.Role && p.Type != wire.PeerInfo {
			continue
		}
		if p.Type == wire.Data {
			r.rxMu.Lock()
			if r.rxPackets == 0 {
				r.rxFirst = at
			}
			r.rxPackets++
			r.rxBytes += int64(n)
			r.rxLast = at
			r.rxMu.Unlock()
			continue
		}
		p.Body = append([]byte(nil), p.Body...)
		r.in <- inbound{from: from, at: at, pkt: p}
	}
}

func never() bool { return false }

// pump handles incoming packets until done() is true (returns true) or
// timeout passes (returns done()). With tick > 0, onTick runs right away and
// then every tick.
func (r *runner) pump(timeout, tick time.Duration, onTick func(), done func() bool) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var ticks <-chan time.Time
	if tick > 0 {
		t := time.NewTicker(tick)
		defer t.Stop()
		ticks = t.C
		onTick()
	}
	for !done() {
		select {
		case m, ok := <-r.in:
			if !ok {
				return done()
			}
			r.handle(m)
		case <-ticks:
			onTick()
		case <-deadline.C:
			return done()
		}
	}
	return true
}

// handle reacts to one packet. Replies (PunchAck, Pong, BarrierAck) are sent
// in every step, so a peer that is one step behind still gets its answers.
func (r *runner) handle(m inbound) {
	if m.stun != nil {
		if a, ok := stun.Parse(m.stun, r.stunTx); ok && !r.stunGot.IsValid() {
			r.stunGot = a
			r.res.STUNMs = ptr(ms(m.at))
		}
		return
	}
	p := m.pkt
	switch p.Type {
	case wire.PeerInfo:
		self, peer, ok := wire.ParseAddrs(p.Body)
		if !ok || m.from != r.rdv || r.peerInfo {
			return
		}
		r.peerInfo, r.peerRdv = true, peer
		r.res.RdvAddr, r.res.PeerRdvAddr = ptr(self.String()), ptr(peer.String())
		r.res.RendezvousMs = ptr(ms(m.at))
		if r.stunGot.IsValid() {
			r.res.MappingVaries = ptr(r.stunGot != self)
		}
		if !r.remote.IsValid() { // a Punch may already have taught us a better one
			r.remote = peer
		}
		r.logf("rendezvous: we are %v, peer is %v", self, peer)
	case wire.Punch:
		r.res.PunchesRecv++
		r.learn(m.from)
		r.sendTo(m.from, wire.PunchAck, p.Seq, nil)
	case wire.PunchAck:
		r.learn(m.from)
		if !r.gotAck {
			r.gotAck, r.ackAt = true, m.at
		}
	case wire.Ping:
		r.sendTo(m.from, wire.Pong, p.Seq, p.Body)
	case wire.Pong:
		sentAt := time.Duration(wire.ParseU64(p.Body))
		if _, dup := r.rtts[p.Seq]; !dup && p.Seq < uint64(r.cfg.Pings) {
			r.rtts[p.Seq] = ms(m.at - sentAt)
		}
	case wire.Barrier:
		r.peerValue[p.Seq] = wire.ParseU64(p.Body)
		if r.reached >= p.Seq {
			r.sendTo(m.from, wire.BarrierAck, p.Seq, wire.U64(r.myValue[p.Seq]))
		}
	case wire.BarrierAck:
		r.peerValue[p.Seq] = wire.ParseU64(p.Body)
	}
}

// learn switches to the address the peer's packets actually come from. Behind
// an endpoint-dependent NAT this differs from what the rendezvous saw.
func (r *runner) learn(from netip.AddrPort) {
	if from != r.remote {
		if r.remote.IsValid() {
			r.logf("punch: peer answers from %v, switching from %v", from, r.remote)
		}
		r.remote = from
	}
}

func (r *runner) sendTo(to netip.AddrPort, t wire.Type, seq uint64, body []byte) {
	p := wire.Packet{Type: t, Role: r.cfg.Role, Session: r.cfg.Session, Seq: seq, Body: body}
	r.conn.WriteToUDPAddrPort(p.Append(nil), to)
}

// barrier waits until the peer has also reached step. Both sides keep
// announcing it; whoever arrives first answers the latecomer with BarrierAck.
func (r *runner) barrier(step, value uint64, timeout time.Duration) error {
	r.reached, r.myValue[step] = step, value
	ok := r.pump(timeout, 50*time.Millisecond,
		func() { r.sendTo(r.remote, wire.Barrier, step, wire.U64(value)) },
		func() bool { _, ok := r.peerValue[step]; return ok })
	if !ok {
		return fmt.Errorf("peer did not reach step %d within %v", step, timeout)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Steps
// ---------------------------------------------------------------------------

func (r *runner) doSTUN() error {
	server, err := resolve(r.cfg.STUN)
	if err != nil {
		return err
	}
	r.stunTx = stun.NewTxID()
	req := stun.Request(r.stunTx)
	ok := r.pump(r.cfg.STUNTimeout, 250*time.Millisecond,
		func() { r.conn.WriteToUDPAddrPort(req, server) },
		func() bool { return r.stunGot.IsValid() })
	if !ok {
		return fmt.Errorf("no reply from %v", server)
	}
	r.res.STUNAddr = ptr(r.stunGot.String())
	r.logf("stun: public address %v", r.stunGot)
	return nil
}

func (r *runner) pingPhase() {
	sent := 0
	r.pump(time.Duration(r.cfg.Pings+1)*r.cfg.PingInterval+r.cfg.StepTimeout, r.cfg.PingInterval,
		func() {
			if sent < r.cfg.Pings {
				r.sendTo(r.remote, wire.Ping, uint64(sent), wire.U64(uint64(time.Since(r.t0))))
				sent++
			}
		},
		func() bool { return sent >= r.cfg.Pings })
	r.pump(r.cfg.PingGrace, 0, nil, func() bool { return len(r.rtts) >= r.cfg.Pings })

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
	s := append([]float64(nil), r.res.RTTs...)
	sort.Float64s(s)
	mean := 0.0
	for _, v := range s {
		mean += v
	}
	mean /= float64(len(s))
	r.res.RTTMean, r.res.RTTMin, r.res.RTTMax = ptr(mean), ptr(s[0]), ptr(s[len(s)-1])
	r.res.RTTP50, r.res.RTTP95 = ptr(percentile(s, 50)), ptr(percentile(s, 95))
	if len(s) > 1 {
		v := 0.0
		for _, x := range s {
			v += (x - mean) * (x - mean)
		}
		r.res.RTTStdev = ptr(math.Sqrt(v / float64(len(s)-1)))
	}
	r.logf("ping: %d/%d replies, median %.2f ms", len(s), sent, *r.res.RTTP50)
}

// sendData sends Size-byte Data packets at RateMbit for Duration, paced every
// millisecond against the clock so the average rate stays exact.
func (r *runner) sendData() uint64 {
	size := max(r.cfg.Size, wire.HeaderLen)
	pps := r.cfg.RateMbit * 1e6 / float64(size*8)
	pkt := wire.Packet{Type: wire.Data, Role: r.cfg.Role, Session: r.cfg.Session,
		Body: make([]byte, size-wire.HeaderLen)}.Append(nil)
	var sent uint64
	start := time.Now()
	r.pump(r.cfg.Duration+time.Second, time.Millisecond,
		func() {
			due := uint64(math.Min(time.Since(start).Seconds(), r.cfg.Duration.Seconds()) * pps)
			for ; sent < due; sent++ {
				putSeq(pkt, sent)
				r.conn.WriteToUDPAddrPort(pkt, r.remote) // errors count as loss
			}
		},
		func() bool { return time.Since(start) >= r.cfg.Duration })
	elapsed := time.Since(start).Seconds()
	r.res.TxPackets = ptr(int64(sent))
	r.res.TxMbit = ptr(float64(sent) * float64(size) * 8 / elapsed / 1e6)
	r.logf("throughput: sent %d packets (%.2f Mbit/s)", sent, *r.res.TxMbit)
	return sent
}

func (r *runner) recordRx(peerSent int64) {
	r.rxMu.Lock()
	n, bytes, window := r.rxPackets, r.rxBytes, (r.rxLast - r.rxFirst).Seconds()
	r.rxMu.Unlock()
	r.res.RxPackets, r.res.RxPeerSent = ptr(n), ptr(peerSent)
	if peerSent > 0 {
		r.res.RxLoss = ptr(1 - float64(n)/float64(peerSent))
	}
	if n >= 2 && window > 0 {
		// n packets span n-1 inter-arrival gaps; count n-1 packets' worth.
		frac := float64(n-1) / float64(n)
		r.res.RxMbit = ptr(float64(bytes) * frac * 8 / window / 1e6)
		r.res.RxIPMbit = ptr(float64(bytes+n*ipUDPHeader) * frac * 8 / window / 1e6)
		r.logf("throughput: received %d/%d packets, %.2f Mbit/s", n, peerSent, *r.res.RxMbit)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func putSeq(pkt []byte, seq uint64) {
	for i := 0; i < 8; i++ {
		pkt[16+i] = byte(seq >> (56 - 8*i))
	}
}

func resolve(hostport string) (netip.AddrPort, error) {
	a, err := net.ResolveUDPAddr("udp4", hostport)
	if err != nil {
		return netip.AddrPort{}, err
	}
	ap := a.AddrPort()
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), nil
}

func percentile(sorted []float64, p float64) float64 {
	k := float64(len(sorted)-1) * p / 100
	lo := int(k)
	hi := min(lo+1, len(sorted)-1)
	return sorted[lo] + (sorted[hi]-sorted[lo])*(k-float64(lo))
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func ptr[T any](v T) *T { return &v }

func (r *runner) logf(format string, args ...any) {
	if r.cfg.Logf != nil {
		r.cfg.Logf("[%c] "+format, append([]any{r.cfg.Role}, args...)...)
	}
}

