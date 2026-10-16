package peer

import (
	"p2ptestbed/p2p/internal/wire"
	"testing"
)

// Only the rendezvous (ID 0, control types only) and the introduced member
// get through; our own reflected packets and strangers do not.
func TestAccepts(t *testing.T) {
	r := &runner{id: 1}
	r.peerID.Store(2)
	for _, c := range []struct {
		typ  wire.Type
		from uint64
		want bool
	}{
		{wire.PeerInfo, 0, true}, {wire.PeerLeft, 0, true}, {wire.PeerInfo, 2, false},
		{wire.Punch, 2, true}, {wire.Data, 2, true},
		{wire.Punch, 0, false}, {wire.Punch, 1, false}, {wire.Punch, 3, false},
	} {
		if got := r.accepts(wire.Packet{Type: c.typ, From: c.from}); got != c.want {
			t.Errorf("type %d from %d: accepted %v, want %v", c.typ, c.from, got, c.want)
		}
	}
	if (&runner{id: 1}).accepts(wire.Packet{Type: wire.Punch, From: 2}) {
		t.Error("accepted a peer packet before any introduction")
	}
}
