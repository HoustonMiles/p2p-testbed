package peer

import (
	"time"

	"p2ptestbed/p2p/internal/wire"
)

// Handoff mode: every link hops where hopping holds and goes through the
// relay where it does not. Each link binds its relay channel first and runs
// its setup through it: the Connected barrier (carrying whether we
// punched), the Noise handshake and the Secured barrier (carrying whether
// our NAT keeps ports). Between the bind and the handshake it punches the
// direct path. Both sides then decide from the same two pairs of values,
// which the relay always delivers, so they always pick the same path:
//
//	both punched, both NATs keep ports   hop (the relay stays bound)
//	either punch failed                  relay, reason "punch"
//	either NAT changes ports             relay, reason "ports"
//	the first hop failed                 relay, reason "hop"
//
// A hopping link keeps its relay channel. Once it has hopped, it moves to
// the relay (pivot) when either
//
//	missed   DirectConfig.PivotAfter hops in a row brought nothing from the member
//	member   the member's own traffic arrives through the relay, sealed: it moved
//
// so whichever side notices first, the other follows within a round trip.
// That also mends a pair split at setup.
//
// TODO(paper §5 Dynamic Handoff Mode): a link never moves back from the
// relay to hopping, and a link that cannot punch waits out the punch
// timeout before it is usable; starting on the relay and moving to hopping
// once the punch works would fix both.
//
// TODO(paper §5 Dynamic Handoff Mode): flood indicators (drain cutoffs,
// auth failures) as a second trigger, once Experiment C shows what a flood
// looks like before hops are missed.

// handoffReasons explains each reason a link stays on the relay.
var handoffReasons = map[string]string{
	"punch": "the punch failed",
	"ports": "a NAT changes ports",
	"hop":   "the first hop failed",
}

// reachBoth binds the relay channel, which then carries the link's setup,
// and punches the direct path the link may hop on. A failed punch is not
// the end of the link: the relay carries it.
func (l *link) reachBoth() bool {
	if !l.bindRelay() {
		return false
	}
	l.punch()
	return true
}

// choosePath puts the secured link on its path: hopping when both sides
// punched and both NATs keep ports, the relay otherwise.
func (l *link) choosePath() bool {
	reason := l.handoffReason()
	if reason == "" && l.tryHopping() {
		l.logf("handoff: hopping")
		return true
	}
	if reason == "" {
		reason = "hop"
	}
	l.res.HandoffReason = reason
	l.logf("handoff: staying on the relay (%s)", handoffReasons[reason])
	return true
}

// handoffReason is why the link cannot hop, or "" if it can: the member's
// Connected barrier carried whether it punched, its Secured barrier
// whether its NAT keeps ports.
func (l *link) handoffReason() string {
	possible := l.notePorts()
	switch {
	case !l.gotAck || l.peerValue[stepConnected] == 0:
		return "punch"
	case !possible && !l.r.cfg.Direct.HopForce:
		return "ports"
	}
	return ""
}

// tryHopping starts the hopper and waits for a first hop both ways. If
// none comes, the hopper closes (keeping its counters for the row) and
// the link's traffic stays on the relay.
func (l *link) tryHopping() bool {
	if err := l.startHopper(); err != nil {
		l.logf("hop: %v", err)
		return false
	}
	if err := l.firstHop(); err != nil {
		l.logf("hop: no contact through the hopper: %v", err)
		l.dropHopper()
		return false
	}
	l.watching.Store(true)
	return true
}

// dropHopper closes the hopper, keeping its counters for the row; the
// link's traffic then goes through the relay.
func (l *link) dropHopper() {
	st := l.hopper.Stats()
	l.hopStats = &st
	l.hopper.Close()
	l.hopper = nil
}

// hopWatcher counts missed hops in a row, on the hopper's goroutine, and
// tells the link's goroutine when hopping has stopped holding.
func (l *link) hopWatcher() func(ok bool) {
	if !l.r.cfg.Handoff {
		return nil
	}
	return func(ok bool) {
		if ok || !l.watching.Load() {
			l.missedHops.Store(0)
			return
		}
		if l.missedHops.Add(1) == int64(l.r.pivotAfter()) && !l.askPivot(time.Since(l.r.t0), "missed") {
			l.missedHops.Add(-1) // a full queue: the next missed hop asks again
		}
	}
}

// askPivot asks the link's goroutine, from another goroutine, to pivot.
func (l *link) askPivot(at time.Duration, why string) bool {
	select {
	case l.in <- inbound{pivot: why, at: at}:
		return true
	default:
		return false
	}
}

// movedToRelay: m is the member's traffic after setup, sealed and through
// the relay, so the member has moved there. Its setup messages up to the
// first hop may still arrive late through the relay; they do not count.
func (m inbound) movedToRelay() bool {
	if !m.sealed {
		return false
	}
	switch m.pkt.Type {
	case wire.Barrier, wire.BarrierAck:
		return m.pkt.Seq > stepHopping
	case wire.Handshake, wire.Punch, wire.PunchAck:
		return false
	}
	return true
}

// pivot moves the hopping link to the relay. Its channel is bound and its
// session seals, so only where packets go changes; the member opens what
// comes through the relay at any time, so it keeps receiving even before
// it moves too.
func (l *link) pivot(at time.Duration, why string) {
	if l.hopper == nil || !l.watching.Load() {
		return
	}
	l.logf("handoff: moving to the relay (%s)", pivotReasons[why])
	l.watching.Store(false)
	l.dropHopper()
	l.res.Pivoted, l.res.PivotMs, l.res.PivotReason = true, ptr(ms(at)), why
}

var pivotReasons = map[string]string{
	"missed": "hops missed in a row",
	"member": "the member's traffic comes through the relay",
}

func (r *runner) pivotAfter() int {
	if n := r.cfg.Direct.PivotAfter; n > 0 {
		return n
	}
	return 3
}

// hopsBlocked is the hoppers' fault injection, for tests.
func (r *runner) hopsBlocked() func() bool {
	after := r.cfg.Direct.blockHopsAfter
	if after == 0 {
		return nil
	}
	return func() bool { return time.Since(r.t0) >= after }
}
