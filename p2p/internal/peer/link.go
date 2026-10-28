package peer

import (
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"p2ptestbed/p2p/internal/direct"
	"p2ptestbed/p2p/internal/noise"
	"p2ptestbed/p2p/internal/wire"
)

// link is this peer's side of the link with one other member of the room.
// Its own goroutine (run) takes it through every step, so a slow or dead
// link holds up only itself. Only that goroutine touches it, except rx,
// which the readers count, and left, which the main loop sets.
type link struct {
	r       *runner
	peerID  uint64
	rdvKey  []byte         // the peer's static key, as the rendezvous announced it
	rdvAddr netip.AddrPort // the peer's address, as the rendezvous saw it
	remote  netip.AddrPort // where its traffic goes; updated by learn
	res     LinkResult

	peerRelay netip.AddrPort                // relayed mode: the member's relayed address
	channel   uint16                        // relayed mode: our TURN channel to it
	bound     bool                          // the channel is bound
	bindErr   error                         // the relay refused to bind it
	opener    atomic.Pointer[noise.Session] // relayed + direct: our session, once the handshake is done: opens the member's packets
	sealed    atomic.Pointer[noise.Session] // the same, once both sides hold it: seals our packets

	in     chan inbound  // the member's packets, routed here by deliver
	pinged chan struct{} // to the main loop: our pings are over (or the link ended)
	turn   chan struct{} // the main loop: our throughput test may start
	tested chan struct{} // to the main loop: our test is over (or skipped)

	err     error       // the link failed; its row carries the error
	stopped bool        // the link ended early with a result (no punch, no hop)
	left    atomic.Bool // the rendezvous said the member left

	gotAck bool
	ackAt  time.Duration
	barrierState
	rtts map[uint64]float64

	rxMu sync.Mutex // the reader goroutines count Data here
	rx   rxCounts

	hs         *noise.Handshake
	handshakeT time.Duration  // when our handshake began, since start
	hopper     *direct.Hopper // once set, all traffic to the member goes through it
	hopStats   *direct.Stats  // frozen when the link's measurements end
}

func newLink(r *runner, in wire.Intro, at time.Duration) *link {
	l := &link{r: r, peerID: in.PeerID, rdvAddr: in.Peer, remote: in.Peer,
		rdvKey: append([]byte(nil), in.Key...), rtts: map[uint64]float64{}, barrierState: newBarrierState(),
		in: make(chan inbound, 1024), pinged: make(chan struct{}, 1), turn: make(chan struct{}, 1), tested: make(chan struct{}, 1)}
	l.rx.series.every = r.cfg.SampleInterval
	l.res.PeerID = idString(in.PeerID)
	l.res.RendezvousMs = ptr(ms(at))
	l.res.PeerRdvAddr = ptr(in.Peer.String())
	if in.Relay.IsValid() {
		l.peerRelay, l.res.PeerRelayAddr = in.Relay, ptr(in.Relay.String())
	}
	return l
}

// run takes the link through its steps, then waits for its turn at the
// throughput test, then keeps answering the member until the run ends.
func (l *link) run() {
	defer l.r.wg.Done()
	if l.connect() && l.r.cfg.Pings > 0 {
		l.ping()
	}
	l.pinged <- struct{}{}
	l.awaitTurn()
	if l.usable() && l.r.cfg.RateMbit > 0 {
		l.throughputTest()
	}
	l.finish()
	l.tested <- struct{}{}
	l.serve()
}

// connect runs step 3 (a punch, or a channel through the relay) and, in
// direct mode, the Noise handshake and what follows it.
func (l *link) connect() bool {
	if !l.reach() || !l.barrier(barrierWait{step: stepConnected, timeout: l.r.cfg.StepTimeout}) || !l.secure() {
		return false
	}
	l.res.ReadyMs = ptr(ms(time.Since(l.r.t0)))
	return true
}

func (l *link) reach() bool {
	if l.r.relay != nil {
		return l.bindRelay()
	}
	return l.punch()
}

// secure runs the direct channel: hopping on a punched path, or, through
// the relay, the handshake alone. We open sealed packets as soon as our
// handshake is done, and seal ours once the member's barrier shows it is
// done too; it sends that barrier only after its handshake, so we can
// always open what it seals.
func (l *link) secure() bool {
	switch {
	case l.r.cfg.Direct == nil:
		return true
	case l.r.relay == nil:
		return l.startDirect()
	case !l.handshake():
		return false
	}
	l.opener.Store(l.hs.Session())
	if !l.barrier(barrierWait{step: stepSecured, timeout: l.r.cfg.StepTimeout}) {
		return false
	}
	l.sealed.Store(l.hs.Session())
	return true
}

func (l *link) usable() bool { return l.err == nil && !l.stopped }

func (l *link) awaitTurn() {
	for {
		select {
		case <-l.turn:
			return
		case m := <-l.in:
			l.handle(m)
		}
	}
}

// serve answers the member, which may still be finishing a step.
func (l *link) serve() {
	for {
		select {
		case <-l.r.done:
			return
		case m := <-l.in:
			l.handle(m)
		}
	}
}

// testOrder is the order of our throughput tests: links by peer ID, which
// is also the order of their pairs sorted by (lower ID, higher ID). Every
// member walks its pairs in that one global order, so no two members ever
// wait on each other in a cycle.
func (r *runner) testOrder() []*link { return r.sortedLinks() }

// runTests gives each link its throughput turn in testOrder, one at a time,
// so no two tests share this peer's link capacity. The tests start once
// every link's pings are over, so no ping is measured under a test's load.
func (r *runner) runTests() {
	order := r.testOrder()
	for _, l := range order {
		r.waitFor(l.pinged)
	}
	for _, l := range order {
		l.turn <- struct{}{}
		r.waitFor(l.tested)
	}
}

// waitFor handles the servers' packets until c fires.
func (r *runner) waitFor(c <-chan struct{}) {
	for {
		select {
		case <-c:
			return
		case m, ok := <-r.in:
			if !ok {
				<-c
				return
			}
			r.handle(m)
		}
	}
}

type pair struct{ lo, hi uint64 }

func (r *runner) pair(peerID uint64) pair { return pair{min(r.id, peerID), max(r.id, peerID)} }

func pairLess(a, b pair) bool { return a.lo < b.lo || a.lo == b.lo && a.hi < b.hi }

// pairsAhead counts the room's pairs that test before this link's: an upper
// bound on how many tests the peer may run before it reaches ours.
func (l *link) pairsAhead() int {
	ids := []uint64{l.r.id}
	l.r.linksMu.RLock()
	for id := range l.r.links {
		ids = append(ids, id)
	}
	l.r.linksMu.RUnlock()
	ours, n := l.r.pair(l.peerID), 0
	for i := range ids {
		for j := range ids[:i] {
			if pairLess(pair{min(ids[i], ids[j]), max(ids[i], ids[j])}, ours) {
				n++
			}
		}
	}
	return n
}

// finish freezes the link's hop counters once its measurements are over.
// The hopper keeps running, so the member still gets answers while it
// finishes; but a member that is done and gone must not count as missed
// hops while we test other pairs.
func (l *link) finish() {
	if l.hopper != nil && l.hopStats == nil {
		st := l.hopper.Stats()
		l.hopStats = &st
	}
}

func (l *link) fail(err error) {
	l.err = err
	l.res.Error = err.Error()
	l.logf("%v", err)
}

// stop ends the link early: a result, not an error.
func (l *link) stop(why string) {
	l.stopped = true
	l.logf("%s", why)
}

func (l *link) logf(format string, args ...any) {
	l.r.logf("%04x: "+format, append([]any{l.peerID >> 48}, args...)...)
}

// onHop takes the link's decrypted packets, whose payload is a wire packet.
func (l *link) onHop(in direct.Inbound) {
	l.r.deliver(inbound{from: in.From, at: in.At.Sub(l.r.t0), link: l, hop: true}, in.Payload)
}

// reply answers m the way it came, so a member still one step behind
// (before its Hopper is up) keeps getting answers on the punched socket.
func (l *link) reply(m inbound, t wire.Type, seq uint64, body []byte) {
	switch {
	case m.hop && l.hopper != nil:
		l.sendHop(wire.Packet{Type: t, Seq: seq, Body: body})
	case l.r.relay != nil:
		l.send(t, seq, body)
	default:
		l.r.sendTo(m.from, t, seq, body)
	}
}

// send uses the Hopper once it runs, the relay in relayed mode (sealed once
// the session is up), the punched socket otherwise.
func (l *link) send(t wire.Type, seq uint64, body []byte) {
	switch {
	case l.hopper != nil:
		l.sendHop(wire.Packet{Type: t, Seq: seq, Body: body})
	case l.r.relay != nil:
		l.transmit(l.seal(l.packet(t, seq, body)))
	default:
		l.r.sendTo(l.remote, t, seq, body)
	}
}

// sendPlain is for the handshake, which runs before any session: through
// the relay in relayed mode, else to the given address.
func (l *link) sendPlain(to netip.AddrPort, t wire.Type, seq uint64, body []byte) {
	if l.r.relay != nil {
		l.transmit(l.packet(t, seq, body))
		return
	}
	l.r.sendTo(to, t, seq, body)
}

func (l *link) packet(t wire.Type, seq uint64, body []byte) []byte {
	return wire.Packet{Type: t, Room: l.r.cfg.Room, From: l.r.id, Seq: seq, Body: body}.Append(nil)
}

func (l *link) sendHop(p wire.Packet) {
	if l.r.cfg.Direct.dropHopSends {
		return
	}
	p.Room, p.From = l.r.cfg.Room, l.r.id
	l.hopper.Send(p.Append(nil)) // errors count as loss
}

func (l *link) String() string { return fmt.Sprintf("%016x at %v", l.peerID, l.remote) }
