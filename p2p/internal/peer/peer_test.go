package peer

import (
	"net"
	"sync"
	"testing"
	"time"

	"p2ptestbed/p2p/internal/direct"
	"p2ptestbed/p2p/internal/rendezvous"
	"p2ptestbed/p2p/internal/stun"
)

// End to end on loopback, no NAT: a fake STUN server, the real rendezvous
// server and both peers. Checks every step completes and reports sensibly.
func TestLoopback(t *testing.T) {
	results := runPair(t, Config{})
	checkResults(t, results)
}

// Each role's static keypair, made once for all tests.
var testKeys = func() map[byte]direct.Keypair {
	keys := map[byte]direct.Keypair{}
	for _, role := range []byte{'a', 'b', 'x'} { // 'x' belongs to nobody in the trial
		kp, err := direct.NewKeypair()
		if err != nil {
			panic(err)
		}
		keys[role] = kp
	}
	return keys
}()

// directConfig is a direct-mode setup; withKeys gives each peer its own key
// and, with PeerKeyFrom "file", the other's public key.
func directConfig(pattern, peerKeyFrom string) Config {
	return Config{Direct: &DirectConfig{Noise: pattern, PeerKeyFrom: peerKeyFrom,
		HandshakeTimeout: 2 * time.Second, HopInterval: 150 * time.Millisecond}}
}

func withKeys(c *Config) {
	other := 'a' + 'b' - c.Role
	c.Direct.Key = testKeys[c.Role]
	if c.Direct.PeerKeyFrom == "file" {
		c.Direct.PeerKey = testKeys[other].Public
	}
}

// The same run through the direct channel: Noise handshake, then ping and
// throughput over hopping ports. Short slots, so both the ping phase and each
// throughput direction cross several hops.
func TestLoopbackDirect(t *testing.T) {
	results := runPair(t, directConfig("kk", "file"), withKeys)
	checkResults(t, results)
	for role, r := range results {
		t.Logf("%c: hop stats %+v", role, r.Stats)
		if !r.Direct || r.Noise != "kk" || r.HandshakeMs == nil || r.HopPossible == nil || !*r.HopPossible ||
			!r.Hopping || !r.HopConnected {
			t.Errorf("%c: direct channel not up: %+v", role, r)
		}
		if r.HopsOK < 3 || r.HopsMissed > 0 || r.AuthFailed > 0 || r.Replayed > 0 {
			t.Errorf("%c: hops ok %d, missed %d, auth failed %d, replayed %d",
				role, r.HopsOK, r.HopsMissed, r.AuthFailed, r.Replayed)
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
			for role, r := range results {
				if r.Error != "" || !r.HopConnected || r.Noise != c.pattern || r.PeerKeyFrom != c.from {
					t.Errorf("%c: error %q, hop_connected %v, noise %q, peer_key_from %q",
						role, r.Error, r.HopConnected, r.Noise, r.PeerKeyFrom)
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
				if c.Role == 'b' {
					c.Direct.PeerKey = testKeys['x'].Public
				}
			})
			for role, r := range results {
				if r.Error == "" || r.HopConnected {
					t.Errorf("%c: expected a handshake failure, got error %q", role, r.Error)
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
		c.Direct.dropHopSends = c.Role == 'b'
	})
	for role, r := range results {
		if r.Error != "" || !r.Hopping || r.HopConnected || r.PingsSent != 0 {
			t.Errorf("%c: error %q, hopping %v, hop_connected %v, pings sent %d",
				role, r.Error, r.Hopping, r.HopConnected, r.PingsSent)
		}
	}
}

// runPair runs both peers against a fake STUN server and the real rendezvous
// server. extra fills in the direct-mode fields; per (optional) adjusts one
// peer's config.
func runPair(t *testing.T, extra Config, per ...func(*Config)) map[byte]*Result {
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

	results := map[byte]*Result{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, role := range []byte{'a', 'b'} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := Config{
				Role: role, Session: 42, Local: "127.0.0.1:0",
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
				t.Logf("%c: %v", role, err)
			}
			mu.Lock()
			results[role] = res
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results
}

func checkResults(t *testing.T, results map[byte]*Result) {
	t.Helper()
	for role, r := range results {
		if r.Error != "" {
			t.Errorf("%c: %s", role, r.Error)
		}
		if !r.Punched || r.PunchMs == nil {
			t.Errorf("%c: not punched", role)
		}
		if r.STUNAddr == nil || *r.STUNAddr != r.Local || r.MappingVaries == nil || *r.MappingVaries {
			t.Errorf("%c: stun %v, local %v", role, r.STUNAddr, r.Local)
		}
		if r.PeerAddrChanged == nil || *r.PeerAddrChanged {
			t.Errorf("%c: peer address changed without a NAT", role)
		}
		if r.PingsRecv != 20 || r.RTTP50 == nil {
			t.Errorf("%c: %d/20 pings", role, r.PingsRecv)
		}
		if r.TxPackets == nil || *r.TxPackets == 0 {
			t.Errorf("%c: sent nothing", role)
		}
		if r.RxPackets == nil || r.RxPeerSent == nil || *r.RxPackets != *r.RxPeerSent || *r.RxLoss != 0 {
			t.Errorf("%c: received %v of %v", role, r.RxPackets, r.RxPeerSent)
		}
		if r.RxMbit == nil || *r.RxMbit < 3 || *r.RxMbit > 7 {
			t.Errorf("%c: rx rate %v, want about 5 Mbit/s", role, r.RxMbit)
		}
	}
	if *results['a'].TxPackets != *results['b'].RxPeerSent {
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
