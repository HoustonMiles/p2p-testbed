package peer

import (
	"errors"
	"fmt"
	"net/netip"
	"time"

	"p2ptestbed/p2p/internal/rendezvous"
	"p2ptestbed/p2p/internal/stun"
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
// the rendezvous can pass it to the other members. In handoff mode the
// others may learn our own address as well as our relayed one.
func (r *runner) rendezvousClient() (*rendezvous.Client, error) {
	server, err := resolve(r.cfg.Rendezvous)
	if err != nil {
		return nil, fmt.Errorf("rendezvous address: %w", err)
	}
	c := &rendezvous.Client{Server: server, Room: r.cfg.Room, ID: r.id, Handoff: r.cfg.Handoff}
	if r.cfg.Direct != nil {
		c.Key = r.cfg.Direct.Key.Public
	}
	return c, nil
}

// meetAtRendezvous registers until the room's other members are all
// introduced; each member's link starts the moment it is (onPeerInfo), so
// a mesh grows while members are still arriving. Fewer after the timeout
// is not fatal: the run goes on with the members it met.
//
// TODO(paper §5, mesh): once the room is full or the timeout has passed,
// later members are ignored; a long-running peer would keep linking with
// members as they come, and replace those that leave.
func (r *runner) meetAtRendezvous() error {
	register, want := r.rdv.Register(), r.cfg.Peers-1
	r.pump(r.cfg.RendezvousTimeout, 100*time.Millisecond,
		func() { r.conn.WriteToUDPAddrPort(register, r.rdv.Server) },
		func() bool { return len(r.links) >= want })
	r.met = true
	r.res.Members = len(r.links) + 1
	switch n := len(r.links); {
	case n == 0:
		return errors.New("no introduction from the rendezvous server (is another member of the room running?)")
	case n < want:
		r.logf("rendezvous: met %d of the %d other members within %v; going on without the rest", n, want, r.cfg.RendezvousTimeout)
	}
	return nil
}

// keepRegistered refreshes our registration until the run ends, so the
// rendezvous does not tell the other members we left.
func (r *runner) keepRegistered() {
	t := time.NewTicker(r.cfg.RefreshInterval)
	defer t.Stop()
	register := r.rdv.Register()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.conn.WriteToUDPAddrPort(register, r.rdv.Server)
		}
	}
}

// onPeerInfo starts a link for each member introduced during the meeting,
// up to the room's size.
func (r *runner) onPeerInfo(m inbound) {
	in, ok := r.rdv.Introduction(m.from, m.pkt)
	if !ok || r.met || r.links[in.PeerID] != nil || len(r.links) >= r.cfg.Peers-1 {
		return
	}
	if r.res.RdvAddr == nil {
		r.noteMapping(in.Self)
	}
	l := newLink(r, in, m.at)
	r.linksMu.Lock()
	r.links[in.PeerID] = l
	r.linksMu.Unlock()
	r.logf("rendezvous: we are %v, member %016x is %v", in.Self, in.PeerID, in.Peer)
	r.wg.Add(1)
	go l.run()
}

// noteMapping compares the mapping the rendezvous saw with STUN's, before
// the first link needs to know whether our NAT keeps ports.
func (r *runner) noteMapping(self netip.AddrPort) {
	r.res.RdvAddr = ptr(self.String())
	if r.stunGot.IsValid() {
		r.res.MappingVaries = ptr(r.stunGot != self)
	}
	if r.cfg.Direct != nil {
		r.res.KeepsPort = ptr(r.keepsPort())
	}
}

// onPeerLeft marks a member gone; links still waiting on it then fail at
// their next barrier instead of timing out. Finished links keep their result.
func (r *runner) onPeerLeft(m inbound) {
	id, ok := r.rdv.Departure(m.from, m.pkt)
	if l := r.links[id]; ok && l != nil && !l.left.Load() {
		l.left.Store(true)
		l.logf("rendezvous: the member left the room")
	}
}
