package peer

import (
	"net/netip"
	"testing"
	"time"

	"p2ptestbed/p2p/internal/wire"
)

// handoffConfig is handoff mode through the fake relay.
func handoffConfig(relay *fakeRelay) Config {
	cfg := directConfig("kk", "file")
	cfg.Relay, cfg.Handoff = &RelayConfig{Server: relay.addr()}, true
	return cfg
}

// Every link can hop, so every link does: the setup went through the relay,
// the throughput test through the hopper.
func TestLoopbackHandoffHops(t *testing.T) {
	relay := startRelay(t)
	names := []string{"a", "b", "c"}
	links := checkMesh(t, runRoom(t, names, handoffConfig(relay), trustRoom(names)))
	for x := range links {
		for y, r := range links[x] {
			if r.Path != "hop" || r.HopsOK < 3 || r.HopsMissed > 0 {
				t.Errorf("%s->%s: path %q, hops ok %d, missed %d", x, y, r.Path, r.HopsOK, r.HopsMissed)
			}
		}
	}
	if relay.forwarded.Load() == 0 || relay.plainData.Load() != 0 {
		t.Fatalf("the relay forwarded %d packets, %d of them readable Data; want the setup and no Data", relay.forwarded.Load(), relay.plainData.Load())
	}
}

// a and c cannot punch each other, so that link goes through the relay,
// sealed; the links with b hop, and do not wait for it.
func TestLoopbackHandoffRelaysWithoutPunch(t *testing.T) {
	relay := startRelay(t)
	names := []string{"a", "b", "c"}
	addr := map[string]netip.Addr{"a": netip.MustParseAddr("127.0.0.2"), "c": netip.MustParseAddr("127.0.0.4")}
	cfg := handoffConfig(relay)
	cfg.StepTimeout = time.Second
	results := runRoom(t, names, cfg, trustRoom(names), func(c *Config) {
		c.PunchTimeout = 3 * time.Second
		c.unreachable = addr[map[string]string{"a": "c", "c": "a"}[c.Name]]
	})
	links := checkMesh(t, results)
	for x := range links {
		for y, r := range links[x] {
			want := "hop"
			if x+y == "ac" || x+y == "ca" {
				want = "relay"
			}
			if r.Path != want || (want == "relay" && (r.HandoffReason != "punch" || r.Punched)) {
				t.Errorf("%s->%s: path %q (%s), punched %v; want %s", x, y, r.Path, r.HandoffReason, r.Punched, want)
			}
		}
	}
	if relay.plainData.Load() != 0 {
		t.Fatalf("the relay could read %d throughput packets; want only ciphertext", relay.plainData.Load())
	}
}

// a's NAT changes ports, so a and b punch but cannot hop: both go through
// the relay, and both say why.
func TestLoopbackHandoffRelaysWhenPortsChange(t *testing.T) {
	relay := startRelay(t)
	results := runPair(t, handoffConfig(relay), withKeys, func(c *Config) {
		c.changesPorts = c.Name == "a"
	})
	checkResults(t, results)
	for name, r := range results {
		if r.Path != "relay" || r.HandoffReason != "ports" || !r.Punched || r.HopPossible == nil || *r.HopPossible || r.Hopping {
			t.Errorf("%s: path %q (%s), punched %v, hop possible %s, hopping %v", name, r.Path, r.HandoffReason, r.Punched, show(r.HopPossible), r.Hopping)
		}
	}
}

// a's Barriers after the handshake are all lost, so b passes that step
// only on a's BarrierAck. a has passed it already and started hopping; b
// has not, so the ack must go back through the relay, the way b's
// Barrier came (on a long path this happens without any loss).
func TestLoopbackHandoffAnswersThroughTheRelay(t *testing.T) {
	relay := startRelay(t)
	results := runPair(t, handoffConfig(relay), withKeys, func(c *Config) {
		if c.Name == "a" {
			c.dropBarrier = stepSecured
		}
	})
	checkResults(t, results)
	for name, r := range results {
		if r.Path != "hop" {
			t.Errorf("%s: path %q (%s), want hop", name, r.Path, r.HandoffReason)
		}
	}
}

// Handoff mode without the pieces it needs is a usage error.
func TestHandoffNeedsDirectAndRelay(t *testing.T) {
	for _, cfg := range []Config{{Handoff: true, Relay: &RelayConfig{}}, {Handoff: true, Direct: &DirectConfig{HopInterval: time.Second}}} {
		cfg.Name = "a"
		if _, err := Run(cfg); err == nil {
			t.Errorf("%+v: no error", cfg)
		}
	}
}

// a's hoppers go silent 1.2 s in, mid-pings. b stops hearing a and moves
// to the relay after three missed hops; a, which still heard b until then,
// follows as soon as b's pings come through the relay. The pings lost in
// between are the cost; the throughput test after them goes entirely
// through the relay, sealed.
func TestLoopbackHandoffPivotsWhenHoppingStops(t *testing.T) {
	relay := startRelay(t)
	cfg := handoffConfig(relay)
	results := runPair(t, cfg, withKeys, func(c *Config) {
		c.Pings, c.PingInterval = 60, 50*time.Millisecond
		if c.Name == "a" {
			c.Direct.blockHopsAfter = 1200 * time.Millisecond
		}
	})
	a, b := results["a"], results["b"]
	for name, r := range results {
		if r.Error != "" || r.Path != "hop" || !r.Pivoted || r.PivotMs == nil || *r.PivotMs < 1200 {
			t.Fatalf("%s: error %q, path %q, pivoted %v at %s ms; want a hopping link moved to the relay after 1200 ms",
				name, r.Error, r.Path, r.Pivoted, show(r.PivotMs))
		}
		if r.PingsRecv < 40 || r.PingsRecv == 60 {
			t.Errorf("%s: %d/60 pings; want a few lost while the link moved, the rest answered", name, r.PingsRecv)
		}
		if r.RxPackets == nil || r.RxPeerSent == nil || *r.RxPackets != *r.RxPeerSent {
			t.Errorf("%s: received %s of %s after the move", name, show(r.RxPackets), show(r.RxPeerSent))
		}
	}
	t.Logf("pivots: b at %.0f ms, a at %.0f ms", *b.PivotMs, *a.PivotMs)
	if b.PivotReason != "missed" || a.PivotReason != "member" || *a.PivotMs-*b.PivotMs > 200 {
		t.Errorf("b moved at %.0f ms (%s), a at %.0f ms (%s); want b on missed hops, a right after it on b's traffic",
			*b.PivotMs, b.PivotReason, *a.PivotMs, a.PivotReason)
	}
	if relay.plainData.Load() != 0 {
		t.Errorf("the relay could read %d throughput packets; want only ciphertext", relay.plainData.Load())
	}
}

// Only the member's traffic after setup, sealed through the relay, says it
// moved there; its late setup messages and anything unsealed do not.
func TestMovedToRelay(t *testing.T) {
	for _, c := range []struct {
		name  string
		m     inbound
		moved bool
	}{
		{"ping", inbound{sealed: true, pkt: wire.Packet{Type: wire.Ping}}, true},
		{"pong", inbound{sealed: true, pkt: wire.Packet{Type: wire.Pong}}, true},
		{"barrier after pings", inbound{sealed: true, pkt: wire.Packet{Type: wire.Barrier, Seq: stepPinged}}, true},
		{"late ack of the secured barrier", inbound{sealed: true, pkt: wire.Packet{Type: wire.BarrierAck, Seq: stepSecured}}, false},
		{"late hopping barrier", inbound{sealed: true, pkt: wire.Packet{Type: wire.Barrier, Seq: stepHopping}}, false},
		{"handshake", inbound{sealed: true, pkt: wire.Packet{Type: wire.Handshake}}, false},
		{"unsealed ping", inbound{pkt: wire.Packet{Type: wire.Ping}}, false},
	} {
		if got := c.m.movedToRelay(); got != c.moved {
			t.Errorf("%s: moved %v, want %v", c.name, got, c.moved)
		}
	}
}
