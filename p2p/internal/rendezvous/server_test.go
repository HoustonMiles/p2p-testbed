package rendezvous

import (
	"bytes"
	"net"
	"net/netip"
	"testing"
	"time"

	"p2ptestbed/p2p/internal/wire"
)

// client is a fake member: one socket, registering under one ID.
type client struct {
	conn *net.UDPConn
	id   uint64
	room uint64
}

func newClient(t *testing.T, room, id uint64) *client {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &client{conn, id, room}
}

func (c *client) register(server netip.AddrPort) {
	key := bytes.Repeat([]byte{byte(c.id)}, wire.KeySize)
	p := wire.Packet{Type: wire.Register, Room: c.room, From: c.id, Body: key}
	c.conn.WriteToUDPAddrPort(p.Append(nil), server)
}

// collect reads for d and returns the members introduced (ID -> key) and
// the IDs reported as left.
func (c *client) collect(d time.Duration) (intros map[uint64][]byte, left map[uint64]bool) {
	intros, left = map[uint64][]byte{}, map[uint64]bool{}
	buf := make([]byte, 2048)
	c.conn.SetReadDeadline(time.Now().Add(d))
	for {
		n, err := c.conn.Read(buf)
		if err != nil {
			return intros, left
		}
		p, ok := wire.Parse(buf[:n])
		switch {
		case !ok || p.Room != c.room:
		case p.Type == wire.PeerInfo:
			if in, ok := wire.ParseIntro(p.Body); ok {
				intros[in.PeerID] = append([]byte(nil), in.Key...)
			}
		case p.Type == wire.PeerLeft:
			left[wire.ParseU64(p.Body)] = true
		}
	}
}

func startServer(t *testing.T, ttl time.Duration) netip.AddrPort {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go (&Server{Conn: conn, TTL: ttl}).Serve()
	return conn.LocalAddr().(*net.UDPAddr).AddrPort()
}

// Every member of a room learns every other member, with its key, and
// nothing about other rooms.
func TestRoomIntroducesEveryMember(t *testing.T) {
	server := startServer(t, 5*time.Second)
	members := []*client{newClient(t, 1, 10), newClient(t, 1, 11), newClient(t, 1, 12)}
	outsider := newClient(t, 2, 20)
	for _, c := range append(members, outsider) {
		c.register(server)
	}
	for _, c := range members {
		intros, _ := c.collect(200 * time.Millisecond)
		if len(intros) != 2 || intros[c.id] != nil || intros[20] != nil {
			t.Errorf("%d was introduced to %v, want the two other members of room 1", c.id, keys(intros))
		}
		for id, key := range intros {
			if !bytes.Equal(key, bytes.Repeat([]byte{byte(id)}, wire.KeySize)) {
				t.Errorf("%d: wrong key for %d", c.id, id)
			}
		}
	}
	if intros, _ := outsider.collect(50 * time.Millisecond); len(intros) != 0 {
		t.Errorf("the only member of room 2 was introduced to %v", keys(intros))
	}
}

// A member that stops registering is dropped after the TTL and the others
// are told; it is not introduced to anyone afterwards.
func TestMemberLeaves(t *testing.T) {
	ttl := 200 * time.Millisecond
	server := startServer(t, ttl)
	a, b, gone := newClient(t, 1, 10), newClient(t, 1, 11), newClient(t, 1, 12)
	gone.register(server)
	deadline := time.Now().Add(3 * ttl)
	left := map[uint64]bool{}
	for time.Now().Before(deadline) {
		a.register(server)
		b.register(server)
		_, l := a.collect(ttl / 4)
		for id := range l {
			left[id] = true
		}
	}
	if !left[12] || left[11] {
		t.Fatalf("left: %v, want only 12", left)
	}
	late := newClient(t, 1, 13)
	late.register(server)
	if intros, _ := late.collect(50 * time.Millisecond); intros[12] != nil || len(intros) != 2 {
		t.Errorf("a late member was introduced to %v, want 10 and 11", keys(intros))
	}
}

func keys(m map[uint64][]byte) []uint64 {
	var out []uint64
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A member in relayed mode registers its relayed address with its key; the
// others are introduced to both, through the real Client's Register, and
// never to its own address.
func TestRelayedAddressIsPassedOn(t *testing.T) {
	server := startServer(t, 5*time.Second)
	relay := netip.MustParseAddrPort("198.18.0.102:50001")
	a, b := newClient(t, 1, 10), newClient(t, 1, 11)
	reg := (&Client{Server: server, Room: 1, ID: 10, Key: bytes.Repeat([]byte{1}, wire.KeySize), Relay: relay}).Register()
	a.conn.WriteToUDPAddrPort(reg, server)
	b.register(server)
	buf := make([]byte, 2048)
	b.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		n, err := b.conn.Read(buf)
		if err != nil {
			t.Fatal("b was never introduced to a")
		}
		p, _ := wire.Parse(buf[:n])
		if in, ok := wire.ParseIntro(p.Body); ok && p.Type == wire.PeerInfo && in.PeerID == 10 {
			if in.Relay != relay || !bytes.Equal(in.Key, bytes.Repeat([]byte{1}, wire.KeySize)) {
				t.Fatalf("a introduced with %+v", in.Extras)
			}
			if in.Peer != relay {
				t.Fatalf("a's own address %v was handed out; want only its relayed address", in.Peer)
			}
			return
		}
	}
}
