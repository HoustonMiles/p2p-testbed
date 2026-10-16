package peer

import (
	"fmt"
	"net/netip"
	"time"

	"p2ptestbed/p2p/internal/direct"
	"p2ptestbed/p2p/internal/stun"
	"p2ptestbed/p2p/internal/wire"
)

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

func (r *runner) onSTUN(m inbound) {
	if a, ok := stun.Parse(m.stun, r.stunTx); ok && !r.stunGot.IsValid() {
		r.stunGot = a
		r.res.STUNMs = ptr(ms(m.at))
	}
}

// onPeerInfo links with the first member introduced; later ones are
// ignored until the link manager (mesh step 3).
func (r *runner) onPeerInfo(m inbound) {
	in, ok := wire.ParseIntro(m.pkt.Body)
	if !ok || m.from != r.rdv || r.peerInfo || in.PeerID == r.id {
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

func (r *runner) onPing(m inbound) { r.reply(m, wire.Pong, m.pkt.Seq, m.pkt.Body) }

func (r *runner) onPong(m inbound) {
	sentAt := time.Duration(wire.ParseU64(m.pkt.Body))
	if _, dup := r.rtts[m.pkt.Seq]; !dup && m.pkt.Seq < uint64(r.cfg.Pings) {
		r.rtts[m.pkt.Seq] = ms(m.at - sentAt)
	}
}

func (r *runner) onBarrier(m inbound) {
	r.peerValue[m.pkt.Seq] = wire.ParseU64(m.pkt.Body)
	if r.reached >= m.pkt.Seq {
		r.reply(m, wire.BarrierAck, m.pkt.Seq, wire.U64(r.myValue[m.pkt.Seq]))
	}
}

func (r *runner) onBarrierAck(m inbound) {
	r.peerValue[m.pkt.Seq] = wire.ParseU64(m.pkt.Body)
	r.acked[m.pkt.Seq] = true
}

// onHandshake answers the peer's handshake messages. Before our own
// handshake starts there is nothing to answer with; the initiator retries.
func (r *runner) onHandshake(m inbound) {
	if r.hs == nil || m.hop {
		return
	}
	reply, err := r.hs.Handle(m.pkt.Body)
	if err != nil {
		r.logf("handshake: dropped a bad message from %v: %v", m.from, err)
		return
	}
	if reply != nil {
		r.sendTo(m.from, wire.Handshake, 0, reply)
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

// barrier waits until the peer has reached step too. Both sides announce it;
// whoever is first answers the latecomer with a BarrierAck. The peer's own
// Barrier is enough to pass, which proves only the peer -> us direction.
func (r *runner) barrier(step, value uint64, timeout time.Duration) error {
	reached := func() bool { _, ok := r.peerValue[step]; return ok }
	return r.waitAt(barrierWait{step, value, timeout, reached})
}

// barrierBothWays passes only on the peer's BarrierAck to our own Barrier,
// proof that both directions work. With one direction broken (a symmetric
// NAT on one side, when hopping is forced) both peers then fail this step
// together, instead of one carrying on alone and failing later.
func (r *runner) barrierBothWays(step uint64, timeout time.Duration) error {
	return r.waitAt(barrierWait{step, 0, timeout, func() bool { return r.acked[step] }})
}

type barrierWait struct {
	step, value uint64
	timeout     time.Duration
	passed      func() bool
}

func (r *runner) waitAt(w barrierWait) error {
	r.reached, r.myValue[w.step] = w.step, w.value
	ok := r.pump(w.timeout, 50*time.Millisecond,
		func() { r.sendPeer(wire.Barrier, w.step, wire.U64(w.value)) },
		w.passed)
	if !ok {
		return fmt.Errorf("peer did not reach step %d within %v", w.step, w.timeout)
	}
	return nil
}
