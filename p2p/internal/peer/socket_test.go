package peer

import (
	"testing"

	"p2ptestbed/p2p/internal/wire"
)

// Only the rendezvous (ID 0, its two message types only) and members we
// were introduced to get through; our own reflected packets and strangers
// do not, and a link's Hopper only passes that member's packets.
func TestAccepts(t *testing.T) {
	r := &runner{id: 1, links: map[uint64]*link{}}
	b, c := &link{peerID: 2}, &link{peerID: 3}
	r.links[2], r.links[3] = b, c
	for _, tc := range []struct {
		typ  wire.Type
		from uint64
		hop  *link // the link whose Hopper it came through, if any
		want *link // the link it is accepted for; nil: dropped (or the rendezvous)
		ok   bool
	}{
		{wire.PeerInfo, 0, nil, nil, true}, {wire.PeerLeft, 0, nil, nil, true},
		{wire.PeerInfo, 2, nil, nil, false}, {wire.PeerInfo, 0, b, nil, false},
		{wire.Punch, 2, nil, b, true}, {wire.Data, 3, nil, c, true},
		{wire.Punch, 0, nil, nil, false}, {wire.Punch, 1, nil, nil, false}, {wire.Punch, 9, nil, nil, false},
		{wire.Ping, 2, b, b, true}, {wire.Ping, 3, b, nil, false},
	} {
		m := inbound{link: tc.hop, hop: tc.hop != nil}
		ok := r.accepts(&m, wire.Packet{Type: tc.typ, From: tc.from})
		if ok != tc.ok || ok && m.link != tc.want {
			t.Errorf("type %d from %d (hop %v): accepted %v for %v, want %v for %v", tc.typ, tc.from, tc.hop != nil, ok, m.link, tc.ok, tc.want)
		}
	}
}

// A flood on the fixed (raw/relay) socket is unparseable junk that belongs to
// no member, so deliver counts it as stray: the plain-socket counterpart of a
// Hopper's hop_unrouted, and what the raw arm of Experiment C measures.
func TestDeliverCountsStrayPackets(t *testing.T) {
	r := &runner{cfg: Config{Room: 7}, links: map[uint64]*link{}}
	r.deliver(inbound{}, []byte("FLOOD and then some junk")) // a flood packet: does not parse
	r.deliver(inbound{}, []byte{1, 2, 3})                     // too short to parse
	if got := r.stray.Load(); got != 2 {
		t.Fatalf("stray = %d, want 2 (both junk packets counted)", got)
	}
}
