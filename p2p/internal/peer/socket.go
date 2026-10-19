package peer

import (
	"net"
	"net/netip"
	"time"

	"p2ptestbed/p2p/internal/stun"
	"p2ptestbed/p2p/internal/wire"
)

func (r *runner) listen() error {
	laddr, err := net.ResolveUDPAddr("udp4", r.cfg.Local)
	if err != nil {
		return err
	}
	if r.conn, err = net.ListenUDP("udp4", laddr); err != nil {
		return err
	}
	r.conn.SetReadBuffer(4 << 20)
	r.conn.SetWriteBuffer(4 << 20)
	r.res.Local = r.conn.LocalAddr().String()
	return nil
}

// linger keeps answering the members, which may still be finishing a step.
func (r *runner) linger() { r.pump(r.cfg.Linger, 0, nil, never) }

// readLoop reads the punched socket on its own goroutine.
func (r *runner) readLoop() {
	defer close(r.in)
	buf := make([]byte, 65536)
	for {
		n, from, err := r.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		at := time.Since(r.t0)
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		if stun.IsMessage(buf[:n]) {
			r.in <- inbound{from: from, at: at, stun: append([]byte(nil), buf[:n]...)}
			continue
		}
		r.deliver(inbound{from: from, at: at}, buf[:n])
	}
}

// deliver finds the packet's link, counts Data on the spot (it can arrive
// at line rate) and queues everything else for the main loop. Packets from
// a link's Hopper come with their link set and must be from that member.
func (r *runner) deliver(m inbound, b []byte) {
	p, ok := wire.Parse(b)
	if !ok || p.Room != r.cfg.Room || !r.accepts(&m, p) {
		return
	}
	if p.Type == wire.Data {
		m.link.countData(m.at, len(b))
		return
	}
	p.Body = append([]byte(nil), p.Body...)
	m.pkt = p
	if !m.hop {
		r.in <- m
		return
	}
	// Never block a Hopper's reader: Close waits for it, and the main loop
	// may have stopped reading. A full queue counts as loss.
	select {
	case r.in <- m:
	default:
	}
}

// accepts packets from the rendezvous (ID 0, its two message types only)
// and from members we were introduced to, and sets m.link for the latter.
func (r *runner) accepts(m *inbound, p wire.Packet) bool {
	if p.Type == wire.PeerInfo || p.Type == wire.PeerLeft {
		return p.From == 0 && !m.hop
	}
	if m.link != nil {
		return p.From == m.link.peerID
	}
	m.link = r.link(p.From)
	return m.link != nil
}

func (r *runner) link(id uint64) *link {
	if id == 0 {
		return nil
	}
	r.linksMu.RLock()
	defer r.linksMu.RUnlock()
	return r.links[id]
}

func never() bool { return false }

// pump handles incoming packets until done() (returns true) or timeout
// (returns done()). With tick > 0, onTick runs at once and then every tick.
func (r *runner) pump(timeout, tick time.Duration, onTick func(), done func() bool) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var ticks <-chan time.Time
	if tick > 0 {
		t := time.NewTicker(tick)
		defer t.Stop()
		ticks = t.C
		onTick()
	}
	for !done() {
		select {
		case m, ok := <-r.in:
			if !ok {
				return done()
			}
			r.handle(m)
		case <-ticks:
			onTick()
		case <-deadline.C:
			return done()
		}
	}
	return true
}

// linkHandlers answer in every step, so a member one step behind still gets
// its PunchAck, Pong or BarrierAck.
var linkHandlers = map[wire.Type]func(*link, inbound){
	wire.Punch:      (*link).onPunch,
	wire.PunchAck:   (*link).onPunchAck,
	wire.Ping:       (*link).onPing,
	wire.Pong:       (*link).onPong,
	wire.Barrier:    (*link).onBarrier,
	wire.BarrierAck: (*link).onBarrierAck,
	wire.Handshake:  (*link).onHandshake,
}

func (r *runner) handle(m inbound) {
	switch {
	case m.stun != nil:
		r.onSTUN(m)
	case m.link != nil:
		if h := linkHandlers[m.pkt.Type]; h != nil {
			h(m.link, m)
		}
	case m.pkt.Type == wire.PeerInfo:
		r.onPeerInfo(m)
	case m.pkt.Type == wire.PeerLeft:
		r.onPeerLeft(m)
	}
}

func (r *runner) sendTo(to netip.AddrPort, t wire.Type, seq uint64, body []byte) {
	p := wire.Packet{Type: t, Room: r.cfg.Room, From: r.id, Seq: seq, Body: body}
	r.conn.WriteToUDPAddrPort(p.Append(nil), to)
}

func resolve(hostport string) (netip.AddrPort, error) {
	a, err := net.ResolveUDPAddr("udp4", hostport)
	if err != nil {
		return netip.AddrPort{}, err
	}
	ap := a.AddrPort()
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), nil
}
