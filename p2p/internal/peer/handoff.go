package peer

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
// TODO(paper §5 Dynamic Handoff Mode): the first hop can fail at one end
// only (its BarrierAck lost), leaving one side hopping and the other on
// the relay; moving a link to the relay while it runs (step 6b) also
// covers that case.
//
// TODO(paper §5 Dynamic Handoff Mode): a link never moves back from the
// relay to hopping.

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
		st := l.hopper.Stats()
		l.hopStats = &st
		l.hopper.Close()
		l.hopper = nil
		return false
	}
	return true
}
