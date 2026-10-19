package peer

import (
	"fmt"
	"net/netip"
	"sync"
	"time"

	"p2ptestbed/p2p/internal/direct"
	"p2ptestbed/p2p/internal/noise"
	"p2ptestbed/p2p/internal/wire"
)

// link is this peer's side of the link with one other member of the room.
// Only the main loop touches it, except rx, which the readers count.
type link struct {
	r       *runner
	peerID  uint64
	rdvKey  []byte         // the peer's static key, as the rendezvous announced it
	rdvAddr netip.AddrPort // the peer's address, as the rendezvous saw it
	remote  netip.AddrPort // where its traffic goes; updated by learn
	res     LinkResult

	err     error // the link failed; its row carries the error
	stopped bool  // the link ended early with a result (no punch, no hop)
	left    bool  // the rendezvous said the member left

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
		rdvKey: append([]byte(nil), in.PeerKey...), rtts: map[uint64]float64{}, barrierState: newBarrierState()}
	l.res.PeerID = idString(in.PeerID)
	l.res.RendezvousMs = ptr(ms(at))
	l.res.PeerRdvAddr = ptr(in.Peer.String())
	return l
}

// testOrder is the order of our throughput tests: active links by peer ID,
// which is also the order of their pairs sorted by (lower ID, higher ID).
// Every member walks its pairs in that one global order, so no two members
// ever wait on each other in a cycle.
func (r *runner) testOrder() []*link { return r.active() }

// active returns the links still taking part, sorted by peer ID.
func (r *runner) active() []*link {
	var ls []*link
	for _, l := range r.sortedLinks() {
		if l.err == nil && !l.stopped {
			ls = append(ls, l)
		}
	}
	return ls
}

type pair struct{ lo, hi uint64 }

func (r *runner) pair(peerID uint64) pair { return pair{min(r.id, peerID), max(r.id, peerID)} }

func pairLess(a, b pair) bool { return a.lo < b.lo || a.lo == b.lo && a.hi < b.hi }

// pairsAhead counts the room's pairs that test before this link's: an upper
// bound on how many tests the peer may run before it reaches ours.
func (l *link) pairsAhead() int {
	ids := []uint64{l.r.id}
	for id := range l.r.links {
		ids = append(ids, id)
	}
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
	if m.hop && l.hopper != nil {
		l.sendHop(wire.Packet{Type: t, Seq: seq, Body: body})
		return
	}
	l.r.sendTo(m.from, t, seq, body)
}

// send uses the Hopper once it runs, the punched socket before.
func (l *link) send(t wire.Type, seq uint64, body []byte) {
	if l.hopper != nil {
		l.sendHop(wire.Packet{Type: t, Seq: seq, Body: body})
		return
	}
	l.r.sendTo(l.remote, t, seq, body)
}

func (l *link) sendHop(p wire.Packet) {
	if l.r.cfg.Direct.dropHopSends {
		return
	}
	p.Room, p.From = l.r.cfg.Room, l.r.id
	l.hopper.Send(p.Append(nil)) // errors count as loss
}

func (l *link) String() string { return fmt.Sprintf("%016x at %v", l.peerID, l.remote) }
