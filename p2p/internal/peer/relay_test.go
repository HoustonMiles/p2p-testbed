package peer

import (
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"

	"p2ptestbed/p2p/internal/stun"
	"p2ptestbed/p2p/internal/turn"
	"p2ptestbed/p2p/internal/wire"
)

// fakeRelay is a TURN server for the loopback tests, without logins:
// Allocate gives each client a relayed address (a socket of its own),
// ChannelBind binds a channel to a peer address, and ChannelData is
// forwarded both ways, as coturn does.
type fakeRelay struct {
	conn      *net.UDPConn
	forwarded atomic.Int64
	plainData atomic.Int64 // throughput packets the relay could read

	mu     sync.Mutex
	allocs map[netip.AddrPort]*allocation // by client address
}

type allocation struct {
	client netip.AddrPort
	sock   *net.UDPConn // the relayed address
	chans  map[uint16]netip.AddrPort
	byPeer map[netip.AddrPort]uint16
}

func startRelay(t *testing.T) *fakeRelay {
	f := &fakeRelay{conn: listen(t), allocs: map[netip.AddrPort]*allocation{}}
	go f.serve(t)
	return f
}

func (f *fakeRelay) addr() string { return f.conn.LocalAddr().String() }

func (f *fakeRelay) serve(t *testing.T) {
	buf := make([]byte, 2048)
	for {
		n, from, err := f.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		f.mu.Lock()
		a := f.allocs[from]
		if ch, data, ok := turn.ParseChannelData(buf[:n]); ok && a != nil {
			if peer, ok := a.chans[ch]; ok {
				a.sock.WriteToUDPAddrPort(data, peer)
			}
		} else if m, ok := stun.Decode(buf[:n]); ok {
			f.request(t, from, a, m)
		}
		f.mu.Unlock()
	}
}

// request answers a TURN request with success (the request type with the
// success class bit set).
func (f *fakeRelay) request(t *testing.T, from netip.AddrPort, a *allocation, m stun.Message) {
	resp := stun.Message{Type: m.Type | 0x0100, ID: m.ID}
	switch {
	case m.Type == 0x0003 && a == nil: // Allocate
		a = &allocation{client: from, sock: listen(t), chans: map[uint16]netip.AddrPort{}, byPeer: map[netip.AddrPort]uint16{}}
		f.allocs[from] = a
		go f.relayBack(a)
		relayed := a.sock.LocalAddr().(*net.UDPAddr).AddrPort()
		resp.Attrs = []stun.Attr{{Type: 0x0016, Value: stun.XORAddress(relayed)}}
	case m.Type == 0x0009 && a != nil: // ChannelBind
		num, _ := m.Get(0x000C)
		v, _ := m.Get(0x0012)
		peer, _ := stun.ParseXORAddress(v)
		ch := uint16(num[0])<<8 | uint16(num[1])
		a.chans[ch], a.byPeer[peer] = peer, ch
	}
	f.conn.WriteToUDPAddrPort(resp.Encode(), from)
}

// relayBack forwards what arrives at a relayed address to its client, on
// the channel bound to the sender; anything from an unbound peer is
// dropped, as without a permission.
func (f *fakeRelay) relayBack(a *allocation) {
	buf := make([]byte, 2048)
	for {
		n, from, err := a.sock.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		f.mu.Lock()
		ch, ok := a.byPeer[from]
		f.mu.Unlock()
		if ok {
			f.forwarded.Add(1)
			if p, ok := wire.Parse(buf[:n]); ok && p.Type == wire.Data {
				f.plainData.Add(1)
			}
			f.conn.WriteToUDPAddrPort(turn.ChannelData(ch, buf[:n]), a.client)
		}
	}
}

// Three peers, every link through the relay: punch's steps and columns,
// a member known only by its relayed address, and everything forwarded.
func TestLoopbackRelayedMesh(t *testing.T) {
	relay := startRelay(t)
	results := runRoom(t, []string{"a", "b", "c"}, Config{Relay: &RelayConfig{Server: relay.addr()}})
	for name, rows := range results {
		if len(rows) != 2 || !rows[0].Relayed || rows[0].RelayAddr == nil {
			t.Fatalf("%s: %d rows, relayed %v", name, len(rows), rows[0].Relayed)
		}
		for _, r := range rows {
			checkRow(t, name+"->"+r.PeerID[:4], r)
		}
	}
	if relay.forwarded.Load() == 0 || relay.plainData.Load() == 0 {
		t.Fatalf("the relay forwarded %d packets, %d of them readable Data; want both", relay.forwarded.Load(), relay.plainData.Load())
	}
}

// The same with Noise: the handshake runs through the relay, and every
// packet after it is sealed, so the relay forwards only ciphertext.
func TestLoopbackRelayedDirect(t *testing.T) {
	relay := startRelay(t)
	names := []string{"a", "b", "c"}
	cfg := directConfig("kk", "file")
	cfg.Relay = &RelayConfig{Server: relay.addr()}
	results := runRoom(t, names, cfg, trustRoom(names))
	for name, rows := range results {
		for _, r := range rows {
			checkRow(t, name+"->"+r.PeerID[:4], r)
			if r.HandshakeMs == nil || r.Hopping {
				t.Errorf("%s: handshake %s, hopping %v; want a handshake and no hopping", name, show(r.HandshakeMs), r.Hopping)
			}
		}
	}
	if n := relay.plainData.Load(); n != 0 {
		t.Fatalf("the relay could read %d throughput packets; want only ciphertext", n)
	}
}

// A member that is not in relayed mode has no relayed address to bind to.
func TestLoopbackRelayNeedsBothSides(t *testing.T) {
	relay := startRelay(t)
	results := runRoom(t, []string{"a", "b"}, Config{PunchTimeout: 0}, func(c *Config) {
		if c.Name == "a" {
			c.Relay = &RelayConfig{Server: relay.addr()}
		}
	})
	if r := results["a"][0]; r.Error == "" || r.ReadyMs != nil {
		t.Errorf("a: error %q, ready %s; want no relayed address to bind", r.Error, show(r.ReadyMs))
	}
}
