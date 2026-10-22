package peer

import (
	"net"
	"net/netip"

	"p2ptestbed/p2p/internal/direct"
)

// startDirect brings up the direct channel on the link: Noise handshake,
// port check, hopper, then a first hop proven both ways. A link that cannot
// hop ends there with that result; in handoff mode it would go through the
// relay instead (handoff.go).
func (l *link) startDirect() bool {
	if !l.handshake() || !l.checkPorts() {
		return false
	}
	if err := l.startHopper(); err != nil {
		l.fail(err)
		return false
	}
	if err := l.firstHop(); err != nil {
		l.stop("hop: no contact through the hopper: " + err.Error())
		return false
	}
	return true
}

// firstHop waits until a hop has worked both ways.
func (l *link) firstHop() error {
	w := barrierWait{step: stepHopping, timeout: l.r.cfg.StepTimeout, bothWays: true}
	if !l.meet(w) {
		return w.errFor(l)
	}
	l.res.HopConnected = true
	return nil
}

// checkPorts exchanges, at a barrier, whether each side's NAT keeps the
// local port as the public port, which hopping needs on both sides.
func (l *link) checkPorts() bool {
	if !l.barrier(barrierWait{step: stepSecured, value: l.r.changesPorts(), timeout: l.r.cfg.StepTimeout}) {
		return false
	}
	if !l.notePorts() && !l.r.cfg.Direct.HopForce {
		l.stop("hop: not possible (a NAT changes ports); handoff mode would use the relay")
		return false
	}
	return true
}

// notePorts reads whether the member's NAT keeps ports off its Secured
// barrier, and reports whether both do, so hopping is possible.
func (l *link) notePorts() bool {
	peerKeeps := l.peerValue[stepSecured] == 0
	possible := *l.r.res.KeepsPort && peerKeeps
	l.res.PeerKeepsPort, l.res.HopPossible = ptr(peerKeeps), ptr(possible)
	return possible
}

// changesPorts is our Secured barrier's value: 1 if our NAT changes ports.
func (r *runner) changesPorts() uint64 {
	if *r.res.KeepsPort {
		return 0
	}
	return 1
}

// keepsPort: STUN saw our local port, and the rendezvous saw the same
// mapping. Without STUN we cannot tell, and assume it does.
func (r *runner) keepsPort() bool {
	if r.cfg.changesPorts {
		return false
	}
	if !r.stunGot.IsValid() {
		return true
	}
	local := r.conn.LocalAddr().(*net.UDPAddr).AddrPort().Port()
	return r.stunGot.Port() == local && (r.res.MappingVaries == nil || !*r.res.MappingVaries)
}

func (l *link) startHopper() error {
	d := l.r.cfg.Direct
	sched := direct.NewSchedule(l.hs.Session().HopKey(), d.HopInterval)
	if d.ClockOffset != 0 || d.ClockDriftPPM != 0 {
		sched.Clock = direct.SkewedClock(d.ClockOffset, d.ClockDriftPPM)
	}
	var err error
	l.hopper, err = direct.NewHopper(direct.Config{
		Session: l.hs.Session(), Schedule: sched,
		PeerIP: l.remote.Addr(), Ports: l.r.ports,
		Lead: d.HopLead, MinOverlap: d.HopMinOverlap, MaxOverlap: d.HopMaxOverlap, DrainIdle: d.HopDrainIdle,
		OnPacket: l.onHop,
		Logf:     l.logf,
	})
	if err != nil {
		return err
	}
	l.res.Hopping = true
	l.logf("hop: started, %v", l.hopper)
	return nil
}

// localIP is the punched socket's address, unless it is bound to all.
func (r *runner) localIP() netip.Addr {
	ip := r.conn.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
	if ip.IsUnspecified() {
		return netip.Addr{}
	}
	return ip
}

func (r *runner) closeHoppers() {
	for _, l := range r.links {
		if l.hopper != nil {
			l.hopper.Close()
		}
	}
	if r.ports != nil {
		r.ports.Close()
	}
}
