package peer

import (
	"bytes"
	"testing"
	"time"
)

// The two-peer runs. End to end on loopback, no NAT (see harness_test.go):
// checks every step completes and reports sensibly.
func TestLoopback(t *testing.T) {
	results := runPair(t, Config{})
	checkResults(t, results)
}

// The same run through the direct channel: Noise handshake, then ping and
// throughput over hopping ports. Short slots, so both the ping phase and each
// throughput direction cross several hops.
func TestLoopbackDirect(t *testing.T) {
	results := runPair(t, directConfig("kk", "file"), withKeys)
	checkResults(t, results)
	for name, r := range results {
		t.Logf("%s: hop stats %+v", name, r.Stats)
		if !r.Direct || r.Noise != "kk" || r.HandshakeMs == nil || r.HopPossible == nil || !*r.HopPossible ||
			!r.Hopping || !r.HopConnected {
			t.Errorf("%s: direct channel not up: %+v", name, r)
		}
		if r.HopsOK < 3 || r.HopsMissed > 0 || r.AuthFailed > 0 || r.Replayed > 0 {
			t.Errorf("%s: hops ok %d, missed %d, auth failed %d, replayed %d",
				name, r.HopsOK, r.HopsMissed, r.AuthFailed, r.Replayed)
		}
	}
}

// The peer with the lower key starts the handshake, whichever role it has.
// Swapping the keys between the roles covers both orders.
func TestLoopbackDirectInitiatorByKey(t *testing.T) {
	aLower := bytes.Compare(testKeys["a"].Public, testKeys["b"].Public) < 0
	swap := func(c *Config) {
		c.Direct.Key, c.Direct.TrustedKeys = testKeys[other(c.Name)], [][]byte{testKeys[c.Name].Public}
	}
	for _, swapped := range []bool{false, true} {
		results := runPair(t, directConfig("kk", "file"), withKeys, func(c *Config) {
			if swapped {
				swap(c)
			}
		})
		a, b := results["a"], results["b"]
		if a.Error != "" || b.Error != "" || a.Initiator == nil || b.Initiator == nil {
			t.Fatalf("swapped %v: errors %q / %q, initiator %v / %v", swapped, a.Error, b.Error, a.Initiator, b.Initiator)
		}
		if want := aLower != swapped; *a.Initiator != want || *b.Initiator == want {
			t.Errorf("swapped %v: a initiator %v, b initiator %v; want a %v", swapped, *a.Initiator, *b.Initiator, want)
		}
	}
}

// Peers already in a network trust every member's key (-peer-key DIR);
// the rendezvous's announcement picks which one the handshake must prove.
func TestLoopbackDirectTrustedSet(t *testing.T) {
	results := runPair(t, directConfig("kk", "file"), withKeys, func(c *Config) {
		c.Direct.TrustedKeys = [][]byte{testKeys["x"].Public, testKeys[other(c.Name)].Public}
	})
	for name, r := range results {
		if r.Error != "" || !r.HopConnected {
			t.Errorf("%s: error %q, hop_connected %v", name, r.Error, r.HopConnected)
		}
	}
}

// The other patterns and key sources, including keys taken from the
// rendezvous (which carries them in Register and PeerInfo).
func TestLoopbackDirectPatterns(t *testing.T) {
	for _, c := range []struct{ pattern, from string }{
		{"ik", "file"}, {"ik", "rendezvous"}, {"xx", "rendezvous"}, {"xx", "none"},
	} {
		t.Run(c.pattern+"-"+c.from, func(t *testing.T) {
			results := runPair(t, directConfig(c.pattern, c.from), withKeys)
			for name, r := range results {
				if r.Error != "" || !r.HopConnected || r.Noise != c.pattern || r.PeerKeyFrom != c.from {
					t.Errorf("%s: error %q, hop_connected %v, noise %q, peer_key_from %q",
						name, r.Error, r.HopConnected, r.Noise, r.PeerKeyFrom)
				}
			}
		})
	}
}

// A peer told to expect someone else's key must not complete the handshake,
// and the run must say so.
func TestLoopbackDirectWrongKey(t *testing.T) {
	for _, pattern := range []string{"kk", "ik", "xx"} {
		t.Run(pattern, func(t *testing.T) {
			cfg := directConfig(pattern, "file")
			cfg.Direct.HandshakeTimeout = 300 * time.Millisecond
			cfg.StepTimeout = 500 * time.Millisecond
			results := runPair(t, cfg, withKeys, func(c *Config) {
				if c.Name == "b" {
					c.Direct.TrustedKeys = [][]byte{testKeys["x"].Public}
				}
			})
			for name, r := range results {
				if r.Error == "" || r.HopConnected {
					t.Errorf("%s: expected a handshake failure, got error %q", name, r.Error)
				}
			}
		})
	}
}

// One-way hop path: b's packets never reach a, a's reach b. This is what a
// symmetric NAT on one side does when hopping is forced. Both sides must
// report hop_connected = false without an error, rather than b carrying on
// alone and failing at a later step.
func TestLoopbackDirectOneWay(t *testing.T) {
	cfg := directConfig("kk", "file")
	cfg.StepTimeout = 500 * time.Millisecond
	results := runPair(t, cfg, withKeys, func(c *Config) {
		c.Direct.dropHopSends = c.Name == "b"
	})
	for name, r := range results {
		if r.Error != "" || !r.Hopping || r.HopConnected || r.PingsSent != 0 {
			t.Errorf("%s: error %q, hopping %v, hop_connected %v, pings sent %d",
				name, r.Error, r.Hopping, r.HopConnected, r.PingsSent)
		}
	}
}
