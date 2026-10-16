package peer

import (
	"errors"
	"fmt"
	"p2ptestbed/p2p/internal/rendezvous"
	"p2ptestbed/p2p/internal/stun"
	"time"
)

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

func (r *runner) onSTUN(m inbound) {
	if a, ok := stun.Parse(m.stun, r.stunTx); ok && !r.stunGot.IsValid() {
		r.stunGot = a
		r.res.STUNMs = ptr(ms(m.at))
	}
}

// rendezvousClient joins our room with our static key in direct mode, so
// the rendezvous can pass it to the peer.
func (r *runner) rendezvousClient() (*rendezvous.Client, error) {
	server, err := resolve(r.cfg.Rendezvous)
	if err != nil {
		return nil, fmt.Errorf("rendezvous address: %w", err)
	}
	c := &rendezvous.Client{Server: server, Room: r.cfg.Room, ID: r.id}
	if r.cfg.Direct != nil {
		c.Key = r.cfg.Direct.Key.Public
	}
	return c, nil
}

// meetAtRendezvous registers until a member is introduced.
func (r *runner) meetAtRendezvous() error {
	register := r.rdv.Register()
	ok := r.pump(r.cfg.RendezvousTimeout, 100*time.Millisecond,
		func() { r.conn.WriteToUDPAddrPort(register, r.rdv.Server) },
		func() bool { return r.peerInfo })
	if !ok {
		return errors.New("no introduction from the rendezvous server (is another member of the room running?)")
	}
	return nil
}

// onPeerInfo links with the first member introduced; later ones are
// ignored until the link manager (mesh step 3).
func (r *runner) onPeerInfo(m inbound) {
	in, ok := r.rdv.Introduction(m.from, m.pkt)
	if !ok || r.peerInfo {
		return
	}
	r.peerInfo, r.peerRdv, r.remote = true, in.Peer, in.Peer
	r.peerID.Store(in.PeerID)
	r.rdvKey = append([]byte(nil), in.PeerKey...)
	r.res.PeerID = ptr(idString(in.PeerID))
	r.res.RdvAddr, r.res.PeerRdvAddr = ptr(in.Self.String()), ptr(in.Peer.String())
	r.res.RendezvousMs = ptr(ms(m.at))
	if r.stunGot.IsValid() {
		r.res.MappingVaries = ptr(r.stunGot != in.Self)
	}
	r.logf("rendezvous: we are %v, peer %016x is %v", in.Self, in.PeerID, in.Peer)
}
