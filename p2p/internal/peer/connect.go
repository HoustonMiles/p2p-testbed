package peer

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"p2ptestbed/p2p/internal/stun"
	"p2ptestbed/p2p/internal/wire"
)

// connect runs steps 1-3 and the barrier after them. It returns false
// without an error when the punch fails, which is a result.
func (r *runner) connect() (bool, error) {
	var err error
	if r.rdv, err = resolve(r.cfg.Rendezvous); err != nil {
		return false, fmt.Errorf("rendezvous address: %w", err)
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

func (r *runner) doSTUN() error {
	server, err := resolve(r.cfg.STUN)
	if err != nil {
		return err
	}
	r.stunTx = stun.NewTxID()
	req := stun.Request(r.stunTx)
	ok := r.pump(r.cfg.STUNTimeout, 250*time.Millisecond,
		func() { r.conn.WriteToUDPAddrPort(req, server) },
		func() bool { return r.stunGot.IsValid() })
	if !ok {
		return fmt.Errorf("no reply from %v", server)
	}
	r.res.STUNAddr = ptr(r.stunGot.String())
	r.logf("stun: public address %v", r.stunGot)
	return nil
}

func (r *runner) meetAtRendezvous() error {
	ok := r.pump(r.cfg.RendezvousTimeout, 100*time.Millisecond,
		func() { r.sendTo(r.rdv, wire.Register, 0, nil) },
		func() bool { return r.peerInfo })
	if !ok {
		return errors.New("no introduction from the rendezvous server (is the other peer running?)")
	}
	return nil
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

func resolve(hostport string) (netip.AddrPort, error) {
	a, err := net.ResolveUDPAddr("udp4", hostport)
	if err != nil {
		return netip.AddrPort{}, err
	}
	ap := a.AddrPort()
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), nil
}

