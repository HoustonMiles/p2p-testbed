package peer

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"

	"p2ptestbed/p2p/internal/direct"
	"p2ptestbed/p2p/internal/rendezvous"
	"p2ptestbed/p2p/internal/stun"
	"p2ptestbed/p2p/internal/wire"
)

// End to end on loopback, no NAT: a fake STUN server, the real rendezvous
// server and both peers. Checks every step completes and reports sensibly.
func TestLoopback(t *testing.T) {
	results := runPair(t, Config{})
	checkResults(t, results)
}

// Each peer's static keypair, made once for all tests.
var testKeys = func() map[string]direct.Keypair {
	keys := map[string]direct.Keypair{}
	for _, name := range []string{"a", "b", "x"} { // x belongs to nobody in the trial
		kp, err := direct.NewKeypair()
		if err != nil {
			panic(err)
		}
		keys[name] = kp
	}
	return keys
}()

func other(name string) string { return map[string]string{"a": "b", "b": "a"}[name] }

// directConfig is a direct-mode setup; withKeys gives each peer its own key
// and, with PeerKeyFrom "file", trusts the other's public key.
func directConfig(pattern, peerKeyFrom string) Config {
	return Config{Direct: &DirectConfig{Noise: pattern, PeerKeyFrom: peerKeyFrom,
		HandshakeTimeout: 2 * time.Second, HopInterval: 150 * time.Millisecond}}
}

func withKeys(c *Config) {
	c.Direct.Key = testKeys[c.Name]
	if c.Direct.PeerKeyFrom == "file" {
		c.Direct.TrustedKeys = [][]byte{testKeys[other(c.Name)].Public}
	}
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

// runPair runs both peers against a fake STUN server and the real rendezvous
// server. extra fills in the direct-mode fields; per (optional) adjusts one
// peer's config.
func runPair(t *testing.T, extra Config, per ...func(*Config)) map[string]*Result {
	t.Helper()
	stunConn := listen(t)
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := stunConn.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			if stun.IsMessage(buf[:n]) {
				stunConn.WriteToUDPAddrPort(stun.Response(stun.TxID(buf[8:20]), from), from)
			}
		}
	}()
	rdvConn := listen(t)
	go (&rendezvous.Server{Conn: rdvConn, TTL: 5 * time.Second}).Serve()

	results := map[string]*Result{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := Config{
				Name: name, Room: 42, Local: "127.0.0.1:0",
				STUN: stunConn.LocalAddr().String(), Rendezvous: rdvConn.LocalAddr().String(),
				STUNTimeout: time.Second, RendezvousTimeout: 5 * time.Second,
				PunchTimeout: 5 * time.Second, PunchInterval: 20 * time.Millisecond,
				StepTimeout: 5 * time.Second,
				Pings:       20, PingInterval: 5 * time.Millisecond, PingGrace: 200 * time.Millisecond,
				RateMbit: 5, Duration: 300 * time.Millisecond, Size: 1000, Drain: 200 * time.Millisecond,
				Linger: 100 * time.Millisecond, Logf: t.Logf,
			}
			if extra.Direct != nil {
				d := *extra.Direct // each peer gets its own copy to adjust
				c.Direct = &d
			}
			if extra.StepTimeout != 0 {
				c.StepTimeout = extra.StepTimeout
			}
			for _, f := range per {
				f(&c)
			}
			res, err := Run(c)
			if err != nil {
				t.Logf("%s: %v", name, err)
			}
			mu.Lock()
			results[name] = res
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results
}

func checkResults(t *testing.T, results map[string]*Result) {
	t.Helper()
	for name, r := range results {
		if r.Error != "" {
			t.Errorf("%s: %s", name, r.Error)
		}
		if !r.Punched || r.PunchMs == nil {
			t.Errorf("%s: not punched", name)
		}
		if r.STUNAddr == nil || *r.STUNAddr != r.Local || r.MappingVaries == nil || *r.MappingVaries {
			t.Errorf("%s: stun %v, local %v", name, r.STUNAddr, r.Local)
		}
		if r.PeerAddrChanged == nil || *r.PeerAddrChanged {
			t.Errorf("%s: peer address changed without a NAT", name)
		}
		if r.PingsRecv != 20 || r.RTTP50 == nil {
			t.Errorf("%s: %d/20 pings", name, r.PingsRecv)
		}
		if r.TxPackets == nil || *r.TxPackets == 0 {
			t.Errorf("%s: sent nothing", name)
		}
		if r.RxPackets == nil || r.RxPeerSent == nil || *r.RxPackets != *r.RxPeerSent || *r.RxLoss != 0 {
			t.Errorf("%s: received %v of %v", name, r.RxPackets, r.RxPeerSent)
		}
		if r.RxMbit == nil || *r.RxMbit < 3 || *r.RxMbit > 7 {
			t.Errorf("%s: rx rate %v, want about 5 Mbit/s", name, r.RxMbit)
		}
	}
	if *results["a"].TxPackets != *results["b"].RxPeerSent {
		t.Error("b did not learn how many packets a sent")
	}
}

func listen(t *testing.T) *net.UDPConn {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
