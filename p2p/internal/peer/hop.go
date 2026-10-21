package peer

import (
	"net"
	"net/netip"

	"p2ptestbed/p2p/internal/direct"
)

// startDirect brings up the direct channel on the link: Noise handshake,
// port check, hopper, then a first hop proven both ways. A link that cannot
// hop ends there with that result.
//
// TODO(paper §5 Dynamic Handoff Mode): a link stopped here is where the
// Handoff Manager should pivot it to the relay instead.
func (l *link) startDirect() bool {
	if !l.handshake() || !l.checkPorts() {
		return false
	}
	if err := l.startHopper(); err != nil {
		l.fail(err)
		return false
	}
	w := barrierWait{step: stepHopping, timeout: l.r.cfg.StepTimeout, bothWays: true}
	if !l.meet(w) {
		l.stop("hop: no contact through the hopper: " + w.errFor(l).Error())
		return false
	}
	l.res.HopConnected = true
	return true
}

// checkPorts exchanges, at a barrier, whether each side's NAT keeps the
// local port as the public port, which hopping needs on both sides.
func (l *link) checkPorts() bool {
	keeps := *l.r.res.KeepsPort
	var changes uint64 // barrier value: 1 = our NAT changes ports
	if !keeps {
		changes = 1
	}
	if !l.barrier(barrierWait{step: stepSecured, value: changes, timeout: l.r.cfg.StepTimeout}) {
		return false
	}
	peerKeeps := l.peerValue[stepSecured] == 0
	l.res.PeerKeepsPort, l.res.HopPossible = ptr(peerKeeps), ptr(keeps && peerKeeps)
	if !(keeps && peerKeeps) && !l.r.cfg.Direct.HopForce {
		l.stop("hop: not possible (a NAT changes ports); the relay would take over")
		return false
	}
	return true
}

// keepsPort: STUN saw our local port, and the rendezvous saw the same
// mapping. Without STUN we cannot tell, and assume it does.
func (r *runner) keepsPort() bool {
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
