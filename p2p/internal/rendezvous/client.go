package rendezvous

import (
	"net/netip"

	"p2ptestbed/p2p/internal/wire"
)

// Client is a member's side of a room: the Register to send, and what the
// server's replies mean. The caller sends and receives on the socket it
// will punch with, since that is the NAT mapping the server must see.
type Client struct {
	Server   netip.AddrPort
	Room, ID uint64
	Key      []byte         // our static public key; nil in raw mode
	Relay    netip.AddrPort // our relayed address on the TURN relay; invalid without one
}

// Register is the packet to send: repeatedly until introduced, then often
// enough that the registration never expires while running.
func (c *Client) Register() []byte {
	body := wire.Extras{Key: c.Key, Relay: c.Relay}.Append(nil)
	return wire.Packet{Type: wire.Register, Room: c.Room, From: c.ID, Body: body}.Append(nil)
}

// Introduction returns the member a PeerInfo from the server introduces.
// It is false for anything else, including an introduction to ourselves.
func (c *Client) Introduction(from netip.AddrPort, p wire.Packet) (wire.Intro, bool) {
	if from != c.Server || p.Type != wire.PeerInfo || p.Room != c.Room {
		return wire.Intro{}, false
	}
	in, ok := wire.ParseIntro(p.Body)
	return in, ok && in.PeerID != c.ID
}

// Departure returns the member a PeerLeft from the server says has left.
func (c *Client) Departure(from netip.AddrPort, p wire.Packet) (uint64, bool) {
	if from != c.Server || p.Type != wire.PeerLeft || p.Room != c.Room || len(p.Body) < 8 {
		return 0, false
	}
	return wire.ParseU64(p.Body), true
}
