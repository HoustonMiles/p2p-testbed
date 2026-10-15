package peer

import (
	"fmt"
	"net"
	"net/netip"
	"time"

	"p2ptestbed/p2p/internal/direct"
	"p2ptestbed/p2p/internal/wire"
)

// startDirect brings up the direct channel: Noise handshake, port check,
// hopper, then a first hop proven both ways. It returns false without an
// error when hopping cannot be used, which is a result.
//
// TODO(paper §5 Dynamic Handoff Mode): each false return here is where the
// Handoff Manager should pivot to the relay instead of ending the run.
func (r *runner) startDirect() (bool, error) {
	if err := r.handshake(); err != nil {
		return false, err
	}
	possible, err := r.checkPorts()
	if err != nil {
		return false, err
	}
	if !possible && !r.cfg.Direct.HopForce {
		r.logf("hop: not possible (a NAT changes ports); the relay would take over")
		return false, nil
	}
	if err := r.startHopper(); err != nil {
		return false, err
	}
	if err := r.barrierBothWays(stepHopping, r.cfg.StepTimeout); err != nil {
		r.logf("hop: no contact through the hopper: %v", err)
		return false, nil
	}
	r.res.HopConnected = true
	return true, nil
}

// handshake runs Noise over the punched socket: a initiates; both sides
// resend their last message until done, and answer the peer's messages
// from onHandshake in whatever step they are. The prologue binds the
// handshake to this trial.
func (r *runner) handshake() error {
	hs, err := r.newHandshake()
	if err != nil {
		return err
	}
	r.hs = hs
	if r.cfg.Role == 'a' {
		if _, err := hs.Start(); err != nil {
			return err
		}
	}
	timeout := r.cfg.Direct.HandshakeTimeout
	if timeout == 0 {
		timeout = r.cfg.PunchTimeout
	}
	began := time.Since(r.t0)
	r.pump(timeout, r.cfg.PunchInterval, r.resendHandshake, func() bool { return hs.Done() || hs.Failed() != nil })
	if err := hs.Failed(); err != nil {
		return fmt.Errorf("noise handshake: %w", err)
	}
	if !hs.Done() {
		return fmt.Errorf("noise handshake (%s): not done after %v (right keys and -noise on both peers?)", r.cfg.Direct.Noise, timeout)
	}
	r.res.HandshakeMs = ptr(ms(time.Since(r.t0) - began))
	r.logf("handshake: %s done in %.1f ms", r.cfg.Direct.Noise, *r.res.HandshakeMs)
	return nil
}

func (r *runner) newHandshake() (*direct.Handshake, error) {
	peerKey, err := r.expectedPeerKey()
	if err != nil {
		return nil, err
	}
	return direct.NewHandshake(direct.HandshakeConfig{
		Pattern:   r.cfg.Direct.Noise,
		Initiator: r.cfg.Role == 'a',
		Static:    r.cfg.Direct.Key,
		PeerKey:   peerKey,
		Prologue:  append([]byte("p2ptestbed direct v2"), wire.U64(r.cfg.Session)...),
	})
}

// expectedPeerKey is the key the peer must prove it holds: provisioned in
// a file, as the rendezvous announced it, or none (trust on first use).
func (r *runner) expectedPeerKey() ([]byte, error) {
	switch d := r.cfg.Direct; d.PeerKeyFrom {
	case "file":
		return d.PeerKey, nil
	case "rendezvous":
		if r.rdvKey == nil {
			return nil, fmt.Errorf("the rendezvous announced no key for the peer")
		}
		return r.rdvKey, nil
	case "none":
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown peer key source %q", d.PeerKeyFrom)
	}
}

func (r *runner) resendHandshake() {
	if last := r.hs.Last(); last != nil && !r.hs.Done() {
		r.sendTo(r.remote, wire.Handshake, 0, last)
	}
}

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
		Session: r.hs.Session(), Schedule: sched, Role: r.cfg.Role,
		PeerIP: r.remote.Addr(), LocalIP: r.localIP(),
		Lead: d.HopLead, MinOverlap: d.HopMinOverlap, MaxOverlap: d.HopMaxOverlap, DrainIdle: d.HopDrainIdle,
		OnPacket: r.onHop,
		Logf:     r.cfg.Logf,
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
