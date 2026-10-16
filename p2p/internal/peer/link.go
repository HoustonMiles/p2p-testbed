package peer

import (
	"net/netip"
	"p2ptestbed/p2p/internal/wire"
	"time"
)

// connect runs steps 1-3 and the barrier after them. It returns false
// without an error when the punch fails, which is a result.
func (r *runner) connect() (bool, error) {
	var err error
	if r.rdv, err = r.rendezvousClient(); err != nil {
		return false, err
	}
	if r.cfg.STUN != "" {
		if err := r.doSTUN(); err != nil {
			r.logf("stun: %v (continuing without it)", err)
		}
	}
	if err := r.meetAtRendezvous(); err != nil {
		return false, err
	}
	if !r.punch() {
		return false, nil
	}
	return true, r.barrier(stepConnected, 0, r.cfg.StepTimeout)
}

// punch sends Punch until a PunchAck arrives. Behind an endpoint-dependent
// NAT the peer answers from another address than the rendezvous saw; learn
// switches to it.
func (r *runner) punch() bool {
	introducedAt := time.Since(r.t0)
	r.pump(r.cfg.PunchTimeout, r.cfg.PunchInterval,
		func() { r.res.PunchesSent++; r.sendTo(r.remote, wire.Punch, uint64(r.res.PunchesSent), nil) },
		func() bool { return r.gotAck })
	r.res.PeerAddr = ptr(r.remote.String())
	r.res.PeerAddrChanged = ptr(r.remote != r.peerRdv)
	if !r.gotAck {
		r.logf("punch: no reply from %v after %v", r.remote, r.cfg.PunchTimeout)
		return false
	}
	r.res.Punched = true
	r.res.PunchMs = ptr(ms(r.ackAt - introducedAt))
	r.logf("punch: connected to %v in %.1f ms", r.remote, *r.res.PunchMs)
	return true
}

func (r *runner) learn(from netip.AddrPort) {
	if from == r.remote {
		return
	}
	if r.remote.IsValid() {
		r.logf("punch: peer answers from %v, switching from %v", from, r.remote)
	}
	r.remote = from
}

func (r *runner) onPunch(m inbound) {
	r.res.PunchesRecv++
	r.learn(m.from)
	r.sendTo(m.from, wire.PunchAck, m.pkt.Seq, nil)
}

func (r *runner) onPunchAck(m inbound) {
	r.learn(m.from)
	if !r.gotAck {
		r.gotAck, r.ackAt = true, m.at
	}
}

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
