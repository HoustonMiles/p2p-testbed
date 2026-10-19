package peer

import (
	"strings"
	"testing"
	"time"
)

// checkMesh checks a clean full mesh: every peer has one good row per other
// member, and in every pair each side learned how much the other sent.
func checkMesh(t *testing.T, results map[string][]*Result) map[string]map[string]*Result {
	t.Helper()
	byID := map[string]string{} // peer ID -> name
	for name, rows := range results {
		byID[rows[0].ID] = name
	}
	links := map[string]map[string]*Result{} // name -> member's name -> row
	for name, rows := range results {
		links[name] = map[string]*Result{}
		if len(rows) != len(results)-1 {
			t.Errorf("%s: %d rows, want %d", name, len(rows), len(results)-1)
		}
		for _, r := range rows {
			links[name][byID[r.PeerID]] = r
			checkRow(t, name+"->"+byID[r.PeerID], r)
			if r.Members != len(results) {
				t.Errorf("%s: members %d, want %d", name, r.Members, len(results))
			}
		}
	}
	for x := range links {
		for y, r := range links[x] {
			if back := links[y][x]; back == nil || back.RxPeerSent == nil || *r.TxPackets != *back.RxPeerSent {
				t.Errorf("%s did not learn how many packets %s sent it", y, x)
			}
		}
		checkOneTestAtATime(t, x, links[x])
	}
	return links
}

// checkOneTestAtATime: no two of a peer's links received Data at the same
// time, so no two tests shared its link capacity.
func checkOneTestAtATime(t *testing.T, name string, links map[string]*Result) {
	t.Helper()
	for y, r := range links {
		for z, s := range links {
			if y < z && r.RxStartMs != nil && s.RxStartMs != nil && *r.RxStartMs <= *s.RxEndMs && *s.RxStartMs <= *r.RxEndMs {
				t.Errorf("%s received from %s (%.0f-%.0f ms) and %s (%.0f-%.0f ms) at the same time",
					name, y, *r.RxStartMs, *r.RxEndMs, z, *s.RxStartMs, *s.RxEndMs)
			}
		}
	}
}

// Three peers in one room link with each other: three pairs, each punched,
// pinged and measured both ways, one pair at a time.
func TestLoopbackMesh(t *testing.T) {
	checkMesh(t, runRoom(t, []string{"a", "b", "c"}, Config{}))
}

// Four peers through the direct channel: six links, each with its own Noise
// session and hop schedule, on each peer's one shared pool of hop sockets.
func TestLoopbackMeshDirect(t *testing.T) {
	names := []string{"a", "b", "c", "d"}
	links := checkMesh(t, runRoom(t, names, directConfig("kk", "file"), trustRoom(names)))
	for x := range links {
		for y, r := range links[x] {
			if !r.HopConnected || r.HopsOK < 3 || r.HopsMissed > 0 || r.AuthFailed > 0 || r.Unrouted > 0 {
				t.Errorf("%s->%s: hop_connected %v, hops ok %d, missed %d, auth failed %d, unrouted %d",
					x, y, r.HopConnected, r.HopsOK, r.HopsMissed, r.AuthFailed, r.Unrouted)
			}
		}
	}
}

// Expecting three members when only two come: after the rendezvous
// timeout the two link with each other and say how many they met.
func TestLoopbackMissingMember(t *testing.T) {
	results := runRoom(t, []string{"a", "b"}, Config{Peers: 3, RendezvousTimeout: time.Second})
	for name, rows := range results {
		if len(rows) != 1 || rows[0].Error != "" || rows[0].Members != 2 || !rows[0].Punched {
			t.Errorf("%s: rows %+v", name, rows)
		}
	}
}

// A member that leaves mid-run: c stops after the punch, so a's and b's
// links to it fail as soon as the rendezvous says c left, well before the
// barrier would time out, and their link with each other is unaffected.
func TestLoopbackMemberLeaves(t *testing.T) {
	cfg := Config{RefreshInterval: 100 * time.Millisecond, StepTimeout: 10 * time.Second}
	began := time.Now()
	results := runRoom(t, []string{"a", "b", "c"}, cfg, func(c *Config) {
		if c.Name == "c" {
			c.Pings, c.RateMbit, c.Linger = 0, 0, 0
		}
	})
	if took := time.Since(began); took > 8*time.Second {
		t.Errorf("took %v: the leave was not noticed before the barrier timed out", took)
	}
	byID := map[string]string{}
	for name, rows := range results {
		byID[rows[0].ID] = name
	}
	for _, name := range []string{"a", "b"} {
		for _, r := range results[name] {
			member := byID[r.PeerID]
			switch {
			case member == "c" && !strings.Contains(r.Error, "left the room"):
				t.Errorf("%s->c: error %q, want that c left", name, r.Error)
			case member != "c":
				checkRow(t, name+"->"+member, r)
			}
		}
	}
}

// A member that comes after the meeting is ignored: a and b, who met each
// other, finish their run as a pair, and c, which waited for both, cannot
// punch them.
func TestLoopbackLateMember(t *testing.T) {
	s := startServers(t, Config{})
	a, b := startPeer(t, s.config(t, "a", 0, Config{})), startPeer(t, s.config(t, "b", 1, Config{}))
	time.Sleep(500 * time.Millisecond) // a and b met at once
	late := s.config(t, "c", 2, Config{Peers: 3, RendezvousTimeout: time.Second})
	late.PunchTimeout = time.Second
	c := startPeer(t, late)
	for name, rows := range map[string][]*Result{"a": <-a, "b": <-b} {
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows, want only its link with the other early member", name, len(rows))
		}
		checkRow(t, name, rows[0])
	}
	for _, r := range <-c {
		if r.Punched {
			t.Errorf("c punched %s, which should have ignored it", r.PeerID)
		}
	}
}

// Every member's throughput order follows one global order of the room's
// pairs, sorted by (lower ID, higher ID), whatever the IDs.
func TestThroughputOrder(t *testing.T) {
	ids := []uint64{0x9a, 0x13, 0x5c, 0xe1, 0x27}
	for _, me := range ids {
		r := &runner{id: me, links: map[uint64]*link{}}
		for _, id := range ids {
			if id != me {
				r.links[id] = &link{r: r, peerID: id}
			}
		}
		order := r.testOrder()
		for i := 1; i < len(order); i++ {
			if !pairLess(r.pair(order[i-1].peerID), r.pair(order[i].peerID)) {
				t.Errorf("%x tests %x before %x, against the global pair order", me, order[i-1].peerID, order[i].peerID)
			}
		}
	}
}
