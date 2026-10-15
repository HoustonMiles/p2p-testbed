package wire

import (
	"bytes"
	"net/netip"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	in := Packet{Type: Ping, Role: 'a', Session: 1 << 40, Seq: 7, Body: U64(123)}
	out, ok := Parse(in.Append(nil))
	if !ok || out.Type != in.Type || out.Role != in.Role || out.Session != in.Session ||
		out.Seq != in.Seq || !bytes.Equal(out.Body, in.Body) {
		t.Fatalf("got %+v, want %+v", out, in)
	}
	self, peer := netip.MustParseAddrPort("203.0.113.10:1000"), netip.MustParseAddrPort("203.0.113.21:65535")
	s, p, ok := ParseAddrs(Addrs(self, peer))
	if !ok || s != self || p != peer {
		t.Fatalf("addrs: got %v %v", s, p)
	}
}

func TestPeerKey(t *testing.T) {
	addrs := Addrs(netip.MustParseAddrPort("203.0.113.10:1"), netip.MustParseAddrPort("203.0.113.20:2"))
	if PeerKey(addrs) != nil {
		t.Fatal("key found in a body without one")
	}
	key := bytes.Repeat([]byte{7}, KeySize)
	if got := PeerKey(append(addrs, key...)); !bytes.Equal(got, key) {
		t.Fatalf("got %x", got)
	}
	if PeerKey(append(addrs, key[:5]...)) != nil {
		t.Fatal("short key accepted")
	}
}
