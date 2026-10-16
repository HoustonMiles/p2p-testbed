package peer

import (
	"net"
	"net/netip"
	"p2ptestbed/p2p/internal/direct"
)

// checkPorts exchanges, at a barrier, whether each side's NAT keeps the
// local port as the public port, which hopping needs on both sides.
func (r *runner) checkPorts() (bool, error) {
	keeps := r.keepsPort()
	var changes uint64 // barrier value: 1 = our NAT changes ports
	if !keeps {
		changes = 1
	}
	if err := r.barrier(stepSecured, changes, r.cfg.StepTimeout); err != nil {
		return false, err
	}
	peerKeeps := r.peerValue[stepSecured] == 0
	r.res.KeepsPort, r.res.PeerKeepsPort = ptr(keeps), ptr(peerKeeps)
	r.res.HopPossible = ptr(keeps && peerKeeps)
	return keeps && peerKeeps, nil
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

func (r *runner) startHopper() error {
	d := r.cfg.Direct
	sched := direct.NewSchedule(r.hs.Session().HopKey(), d.HopInterval)
	if d.ClockOffset != 0 || d.ClockDriftPPM != 0 {
		sched.Clock = direct.SkewedClock(d.ClockOffset, d.ClockDriftPPM)
	}
	var err error
	r.hopper, err = direct.NewHopper(direct.Config{
		Session: r.hs.Session(), Schedule: sched,
		PeerIP: r.remote.Addr(), LocalIP: r.localIP(),
		Lead: d.HopLead, MinOverlap: d.HopMinOverlap, MaxOverlap: d.HopMaxOverlap, DrainIdle: d.HopDrainIdle,
		OnPacket: r.onHop,
		Logf:     r.logf,
	})
	if err != nil {
		return err
	}
	r.res.Hopping = true
	r.logf("hop: started, %v", r.hopper)
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

func (r *runner) closeHopper() {
	if r.hopper != nil {
		r.hopper.Close()
	}
}
