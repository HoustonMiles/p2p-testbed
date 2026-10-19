package rendezvous

import (
	"net/netip"
	"testing"

	"p2ptestbed/p2p/internal/wire"
)

func TestClientIntroduction(t *testing.T) {
	server := netip.MustParseAddrPort("203.0.113.101:7000")
	c := &Client{Server: server, Room: 1, ID: 10}
	intro := func(peerID uint64) wire.Packet {
		body := wire.Intro{Self: netip.MustParseAddrPort("203.0.113.10:1"),
			Peer: netip.MustParseAddrPort("203.0.113.20:2"), PeerID: peerID}.Append(nil)
		return wire.Packet{Type: wire.PeerInfo, Room: 1, Body: body}
	}
	if in, ok := c.Introduction(server, intro(11)); !ok || in.PeerID != 11 {
		t.Fatalf("a real introduction: %+v, %v", in, ok)
	}
	other := intro(11)
	other.Room = 2
	for name, tc := range map[string]struct {
		from netip.AddrPort
		p    wire.Packet
	}{
		"from someone else": {netip.MustParseAddrPort("203.0.113.66:7000"), intro(11)},
		"to ourselves":      {server, intro(10)},
		"another room":      {server, other},
	} {
		if _, ok := c.Introduction(tc.from, tc.p); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestClientDeparture(t *testing.T) {
	server := netip.MustParseAddrPort("203.0.113.101:7000")
	c := &Client{Server: server, Room: 1, ID: 10}
	left := wire.Packet{Type: wire.PeerLeft, Room: 1, Body: wire.U64(11)}
	if id, ok := c.Departure(server, left); !ok || id != 11 {
		t.Fatalf("got %d, %v", id, ok)
	}
	if _, ok := c.Departure(netip.MustParseAddrPort("203.0.113.66:7000"), left); ok {
		t.Error("accepted a PeerLeft from someone else")
	}
	if _, ok := c.Departure(server, wire.Packet{Type: wire.PeerLeft, Room: 1}); ok {
		t.Error("accepted a PeerLeft without an ID")
	}
}

