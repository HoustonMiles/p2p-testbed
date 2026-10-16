package rendezvous

import (
	"net/netip"

	"p2ptestbed/p2p/internal/wire"
)

// Client is a member's side of a room: the Register to send, and what the
// server's replies mean. The caller sends and receives on the socket it
// will punch with, since that is the NAT mapping the server must see.
//
// TODO(paper §5, mesh step 3): keep registering while running, so late
// members are introduced and this member does not expire; report PeerLeft.
type Client struct {
	Server   netip.AddrPort
	Room, ID uint64
	Key      []byte // our static public key; nil in raw mode
}

// Register is the packet to send, repeatedly, until introduced.
func (c *Client) Register() []byte {
	return wire.Packet{Type: wire.Register, Room: c.Room, From: c.ID, Body: c.Key}.Append(nil)
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
