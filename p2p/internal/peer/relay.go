package peer

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"p2ptestbed/p2p/internal/noise"
	"p2ptestbed/p2p/internal/stun"
	"p2ptestbed/p2p/internal/turn"
)

// Relayed mode: every link goes through the TURN relay instead of a hole
// punch. The peer allocates one relayed address before it registers at the
// rendezvous, which passes that address (and only that address) to the
// other members; each link binds a TURN channel to its member's relayed
// address. The member sends to our relayed address, so neither side ever
// sends to the other's own address. With Config.Direct set, the link runs
// the Noise handshake through the relay and seals everything after it, so
// the relay forwards only ciphertext; it does not hop.
//
// TODO(paper §5 Relayed Mode, long sessions): allocations last 10 minutes,
// permissions 5; refresh both for sessions longer than that (the attack
// experiments).

// relay is this peer's allocation, shared by its links. Reader goroutines
// route replies through it, so it locks.
type relay struct {
	server netip.AddrPort
	addr   netip.AddrPort // our relayed address, once allocated
	err    error          // the allocation was refused

	mu       sync.Mutex
	client   turn.Client
	pending  map[stun.TxID]*link // whose reply each request awaits; nil: the runner's
	channels map[uint16]*link    // the link each channel carries
	next     uint16
}

func newRelay(c *RelayConfig) (*relay, error) {
	server, err := resolve(c.Server)
	if err != nil {
		return nil, fmt.Errorf("relay address: %w", err)
	}
	return &relay{server: server, client: turn.Client{Username: c.Username, Password: c.Password},
		pending: map[stun.TxID]*link{}, channels: map[uint16]*link{}}, nil
}

// request builds a TURN request whose reply goes to l (nil: the runner).
func (rl *relay) request(l *link, build func(*turn.Client, stun.TxID) []byte) []byte {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	id := stun.NewTxID()
	rl.pending[id] = l
	return build(&rl.client, id)
}

// reply reads a TURN reply and says who awaits it.
func (rl *relay) reply(b []byte) (turn.Response, *link, bool) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	r, ok := rl.client.Read(b)
	l, waiting := rl.pending[r.ID]
	if !ok || !waiting {
		return r, nil, false
	}
	delete(rl.pending, r.ID)
	return r, l, true
}

func (rl *relay) bind(l *link) uint16 {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	ch := turn.MinChannel + rl.next
	rl.next++
	rl.channels[ch] = l
	return ch
}

func (rl *relay) link(ch uint16) *link {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.channels[ch]
}

// allocate gets our relayed address, before the rendezvous announces it.
func (r *runner) allocate() error {
	rl := r.relay
	send := func() { r.conn.WriteToUDPAddrPort(rl.request(nil, (*turn.Client).Allocate), rl.server) }
	ok := r.pump(r.cfg.STUNTimeout, 250*time.Millisecond, send,
		func() bool { return rl.addr.IsValid() || rl.err != nil })
	switch {
	case rl.err != nil:
		return rl.err
	case !ok:
		return fmt.Errorf("relay: no answer from %v", rl.server)
	}
	r.res.RelayAddr, r.res.RelayMs = ptr(rl.addr.String()), ptr(ms(time.Since(r.t0)))
	r.rdv.Relay = rl.addr
	r.logf("relay: our relayed address is %v", rl.addr)
	return nil
}

// onRelayReply takes the replies to the runner's own requests.
func (r *runner) onRelayReply(resp turn.Response) {
	rl := r.relay
	switch {
	case resp.OK && resp.Relayed.IsValid():
		rl.addr = resp.Relayed
	case resp.Retry: // the server gave us its realm and nonce: ask again, signed
		r.conn.WriteToUDPAddrPort(rl.request(nil, (*turn.Client).Allocate), rl.server)
	case !resp.OK:
		rl.err = fmt.Errorf("relay: allocation refused: %d %s", resp.Code, resp.Reason)
	}
}

// release frees our allocation at the end of the run (best effort).
func (r *runner) release() {
	b := r.relay.request(nil, func(c *turn.Client, id stun.TxID) []byte { return c.Refresh(id, 0) })
	r.conn.WriteToUDPAddrPort(b, r.relay.server)
}

// fromRelay takes what the relay sends us: ChannelData from a member, or a
// reply to a request.
func (r *runner) fromRelay(at time.Duration, b []byte) {
	if ch, data, ok := turn.ParseChannelData(b); ok {
		if l := r.relay.link(ch); l != nil {
			l.fromRelay(at, data)
		}
		return
	}
	resp, l, ok := r.relay.reply(b)
	if !ok {
		return
	}
	m := inbound{at: at, turn: &resp}
	if l == nil {
		r.in <- m
		return
	}
	select {
	case l.in <- m:
	default:
	}
}

// fromRelay opens the member's packet if it is sealed for our session (its
// receiver index says so), and delivers it like any other.
func (l *link) fromRelay(at time.Duration, data []byte) {
	if s := l.opener.Load(); s != nil {
		if idx, ok := noise.ReceiverIndex(data); ok && idx == s.LocalIndex() {
			_, plain, err := s.Open(nil, data)
			if err != nil {
				return // forged or replayed
			}
			data = plain
		}
	}
	l.r.deliver(inbound{from: l.remote, at: at, link: l}, data)
}

// bindRelay binds a channel to the member's relayed address: the relayed
// counterpart of the punch.
func (l *link) bindRelay() bool {
	if !l.peerRelay.IsValid() {
		l.fail(errors.New("relay: the member announced no relayed address (is it in relayed mode?)"))
		return false
	}
	l.channel = l.r.relay.bind(l)
	start := time.Since(l.r.t0)
	l.pump(l.r.cfg.PunchTimeout, l.r.cfg.PunchInterval,
		func() {
			if !l.bound {
				l.sendBind()
			}
		},
		func() bool { return l.bound || l.bindErr != nil })
	switch {
	case l.bindErr != nil:
		l.fail(l.bindErr)
	case !l.bound:
		l.fail(fmt.Errorf("relay: no answer to ChannelBind within %v", l.r.cfg.PunchTimeout))
	default:
		l.res.RelayBindMs = ptr(ms(time.Since(l.r.t0) - start))
		l.logf("relay: channel %#x to %v", l.channel, l.peerRelay)
	}
	return l.bound
}

func (l *link) sendBind() {
	ch, peer := l.channel, l.peerRelay
	b := l.r.relay.request(l, func(c *turn.Client, id stun.TxID) []byte { return c.ChannelBind(id, ch, peer) })
	l.r.conn.WriteToUDPAddrPort(b, l.r.relay.server)
}

func (l *link) onRelayReply(resp turn.Response) {
	switch {
	case resp.OK:
		l.bound = true
	case resp.Retry:
		l.sendBind()
	default:
		l.bindErr = fmt.Errorf("relay: ChannelBind refused: %d %s", resp.Code, resp.Reason)
	}
}

// transmit sends bytes to the member: through its channel when relayed,
// else straight to its address.
func (l *link) transmit(b []byte) {
	if len(b) == 0 { // a seal that failed
		return
	}
	if l.r.relay != nil {
		l.r.conn.WriteToUDPAddrPort(turn.ChannelData(l.channel, b), l.r.relay.server)
		return
	}
	l.r.conn.WriteToUDPAddrPort(b, l.remote)
}

// seal encrypts a packet for the member once the relayed link has its
// Noise session; before that it goes as it is.
func (l *link) seal(pkt []byte) []byte {
	s := l.sealed.Load()
	if s == nil {
		return pkt
	}
	out, err := s.Seal(make([]byte, 0, len(pkt)+noise.Overhead), relayData, pkt)
	if err != nil {
		return nil
	}
	return out
}

// relayData is the Noise packet type of a sealed relayed packet.
const relayData = 1
