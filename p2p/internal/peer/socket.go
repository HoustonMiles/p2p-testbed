package peer

import (
	"net"
	"net/netip"
	"p2ptestbed/p2p/internal/direct"
	"p2ptestbed/p2p/internal/stun"
	"p2ptestbed/p2p/internal/wire"
	"time"
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

// linger keeps answering the peer, which may still be finishing a step.
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

// onHop takes the Hopper's decrypted packets, whose payload is a wire packet.
func (r *runner) onHop(in direct.Inbound) {
	r.deliver(inbound{from: in.From, at: in.At.Sub(r.t0), hop: true}, in.Payload)
}

// deliver counts Data packets on the spot (they can arrive at line rate) and
// queues everything else for the main loop.
func (r *runner) deliver(m inbound, b []byte) {
	p, ok := wire.Parse(b)
	if !ok || p.Room != r.cfg.Room || !r.accepts(p) {
		return
	}
	if p.Type == wire.Data {
		r.countData(m.at, len(b))
		return
	}
	p.Body = append([]byte(nil), p.Body...)
	m.pkt = p
	if !m.hop {
		r.in <- m
		return
	}
	// Never block a Hopper reader: Close waits for them, and the main loop
	// may have stopped reading. A full queue counts as loss.
	select {
	case r.in <- m:
	default:
	}
}

// accepts packets from the rendezvous (ID 0) and from the member we were
// introduced to; nothing from anyone before the introduction.
func (r *runner) accepts(p wire.Packet) bool {
	if p.Type == wire.PeerInfo || p.Type == wire.PeerLeft {
		return p.From == 0
	}
	return p.From != 0 && p.From == r.peerID.Load()
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

// handlers answer in every step, so a peer one step behind still gets
// its PunchAck, Pong or BarrierAck.
var handlers = map[wire.Type]func(*runner, inbound){
	wire.PeerInfo:   (*runner).onPeerInfo,
	wire.Punch:      (*runner).onPunch,
	wire.PunchAck:   (*runner).onPunchAck,
	wire.Ping:       (*runner).onPing,
	wire.Pong:       (*runner).onPong,
	wire.Barrier:    (*runner).onBarrier,
	wire.BarrierAck: (*runner).onBarrierAck,
	wire.Handshake:  (*runner).onHandshake,
}

func (r *runner) handle(m inbound) {
	if m.stun != nil {
		r.onSTUN(m)
		return
	}
	if h := handlers[m.pkt.Type]; h != nil {
		h(r, m)
	}
}

// reply answers m the way it came, so a peer still one step behind (before
// its Hopper is up) keeps getting answers on the punched socket.
func (r *runner) reply(m inbound, t wire.Type, seq uint64, body []byte) {
	if m.hop && r.hopper != nil {
		r.sendHop(wire.Packet{Type: t, Seq: seq, Body: body})
		return
	}
	r.sendTo(m.from, t, seq, body)
}

// sendPeer uses the Hopper once it runs, the punched socket before.
func (r *runner) sendPeer(t wire.Type, seq uint64, body []byte) {
	if r.hopper != nil {
		r.sendHop(wire.Packet{Type: t, Seq: seq, Body: body})
		return
	}
	r.sendTo(r.remote, t, seq, body)
}

func (r *runner) sendHop(p wire.Packet) {
	if r.cfg.Direct.dropHopSends {
		return
	}
	p.Room, p.From = r.cfg.Room, r.id
	r.hopper.Send(p.Append(nil)) // errors count as loss
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
