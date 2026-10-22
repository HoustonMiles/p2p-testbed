package peer

import (
	"fmt"
	"time"

	"p2ptestbed/p2p/internal/wire"
)

// Barrier steps must rise in the order they run on a link: a member answers
// a Barrier for every step up to the one its side of the link has reached.
const (
	stepConnected  = 1
	stepSecured    = 2
	stepHopping    = 3
	stepPinged     = 4
	stepTestReady  = 5 // both sides are at this pair's throughput test
	stepFirstSend  = 6 // the lower ID sends
	stepSecondSend = 7
)

// barrierState is one link's view of the barriers.
type barrierState struct {
	reached   uint64            // highest step our side of the link has reached
	myValue   map[uint64]uint64 // value attached to each of our steps
	peerValue map[uint64]uint64 // steps the member has reached, with its value
	acked     map[uint64]bool   // steps the member answered with a BarrierAck
}

func newBarrierState() barrierState {
	return barrierState{myValue: map[uint64]uint64{}, peerValue: map[uint64]uint64{}, acked: map[uint64]bool{}}
}

// barrierWait is one barrier. A link passes on the member's Barrier, which
// proves only the member -> us direction; with bothWays only on its
// BarrierAck to ours, proof that both directions work.
type barrierWait struct {
	step     uint64
	value    uint64
	timeout  time.Duration
	bothWays bool
}

func (w barrierWait) passed(l *link) bool {
	if w.bothWays {
		return l.acked[w.step]
	}
	_, ok := l.peerValue[w.step]
	return ok
}

// errFor says why l missed the barrier.
func (w barrierWait) errFor(l *link) error {
	if l.left.Load() {
		return fmt.Errorf("the member left the room before step %d", w.step)
	}
	return fmt.Errorf("the member did not reach step %d within %v", w.step, w.timeout)
}

// setupTime is the longest a link can take from the meeting until it is
// ready for its throughput test: every step's timeout in turn. A member may
// still be bringing up a link that tests before ours.
func (r *runner) setupTime() time.Duration {
	c := r.cfg
	t := c.PunchTimeout + c.StepTimeout // punch (relayed: bind), connected
	if c.Handoff {
		t += c.PunchTimeout // bind, then punch
	}
	if c.Direct != nil {
		t += r.handshakeTimeout() + 2*c.StepTimeout // handshake, secured, hopping
	}
	if c.Pings > 0 {
		t += time.Duration(c.Pings+1)*c.PingInterval + c.StepTimeout + c.PingGrace + c.StepTimeout // pings, pinged
	}
	return t
}

// meet waits until l has passed w and reports whether it did within the
// timeout. Both sides announce the step; whoever is first answers the
// latecomer with a BarrierAck. A member that left is not waited for.
func (l *link) meet(w barrierWait) bool {
	l.reached, l.myValue[w.step] = w.step, w.value
	return l.pump(w.timeout, 50*time.Millisecond,
		func() {
			if !w.passed(l) && w.step != l.r.cfg.dropBarrier {
				l.send(wire.Barrier, w.step, wire.U64(w.value))
			}
		},
		func() bool { return w.passed(l) || l.left.Load() }) && w.passed(l)
}

// barrier is meet, and fails the link if the member missed it.
func (l *link) barrier(w barrierWait) bool {
	if !l.meet(w) {
		l.fail(w.errFor(l))
		return false
	}
	return true
}

func (l *link) onBarrier(m inbound) {
	l.peerValue[m.pkt.Seq] = wire.ParseU64(m.pkt.Body)
	if l.reached >= m.pkt.Seq {
		l.reply(m, wire.BarrierAck, m.pkt.Seq, wire.U64(l.myValue[m.pkt.Seq]))
	}
}

func (l *link) onBarrierAck(m inbound) {
	l.peerValue[m.pkt.Seq] = wire.ParseU64(m.pkt.Body)
	l.acked[m.pkt.Seq] = true
}
