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
	if l.left {
		return fmt.Errorf("the member left the room before step %d", w.step)
	}
	return fmt.Errorf("the member did not reach step %d within %v", w.step, w.timeout)
}

// barrier waits until every link in ls has passed w, and returns those that
// did not within the timeout. Both sides announce the step; whoever is
// first answers the latecomer with a BarrierAck. A member that left is not
// waited for.
func (r *runner) barrier(ls []*link, w barrierWait) (missed []*link) {
	for _, l := range ls {
		l.reached, l.myValue[w.step] = w.step, w.value
	}
	waiting := func() []*link {
		var out []*link
		for _, l := range ls {
			if !l.left && !w.passed(l) {
				out = append(out, l)
			}
		}
		return out
	}
	r.pump(w.timeout, 50*time.Millisecond,
		func() {
			for _, l := range waiting() {
				l.send(wire.Barrier, w.step, wire.U64(w.value))
			}
		},
		func() bool { return len(waiting()) == 0 })
	for _, l := range ls {
		if !w.passed(l) {
			missed = append(missed, l)
		}
	}
	return missed
}

// barrierAll runs w on every active link and fails the links that miss it.
func (r *runner) barrierAll(w barrierWait) {
	for _, l := range r.barrier(r.active(), w) {
		l.fail(w.errFor(l))
	}
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
