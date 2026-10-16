package peer

import (
	"fmt"
	"p2ptestbed/p2p/internal/wire"
	"time"
)

// Barrier steps must rise in the order they run: a peer answers a Barrier
// for every step up to the one it has reached.
const (
	stepConnected  = 1
	stepSecured    = 2
	stepHopping    = 3
	stepPinged     = 4
	stepFirstSend  = 5 // the lower ID sends
	stepSecondSend = 6
)

// barrier waits until the peer has reached step too. Both sides announce it;
// whoever is first answers the latecomer with a BarrierAck. The peer's own
// Barrier is enough to pass, which proves only the peer -> us direction.
func (r *runner) barrier(step, value uint64, timeout time.Duration) error {
	reached := func() bool { _, ok := r.peerValue[step]; return ok }
	return r.waitAt(barrierWait{step, value, timeout, reached})
}

// barrierBothWays passes only on the peer's BarrierAck to our own Barrier,
// proof that both directions work. With one direction broken (a symmetric
// NAT on one side, when hopping is forced) both peers then fail this step
// together, instead of one carrying on alone and failing later.
func (r *runner) barrierBothWays(step uint64, timeout time.Duration) error {
	return r.waitAt(barrierWait{step, 0, timeout, func() bool { return r.acked[step] }})
}

type barrierWait struct {
	step, value uint64
	timeout     time.Duration
	passed      func() bool
}

func (r *runner) waitAt(w barrierWait) error {
	r.reached, r.myValue[w.step] = w.step, w.value
	ok := r.pump(w.timeout, 50*time.Millisecond,
		func() { r.sendPeer(wire.Barrier, w.step, wire.U64(w.value)) },
		w.passed)
	if !ok {
		return fmt.Errorf("peer did not reach step %d within %v", w.step, w.timeout)
	}
	return nil
}

func (r *runner) onBarrier(m inbound) {
	r.peerValue[m.pkt.Seq] = wire.ParseU64(m.pkt.Body)
	if r.reached >= m.pkt.Seq {
		r.reply(m, wire.BarrierAck, m.pkt.Seq, wire.U64(r.myValue[m.pkt.Seq]))
	}
}

func (r *runner) onBarrierAck(m inbound) {
	r.peerValue[m.pkt.Seq] = wire.ParseU64(m.pkt.Body)
	r.acked[m.pkt.Seq] = true
}
