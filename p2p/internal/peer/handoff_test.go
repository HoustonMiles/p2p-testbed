package peer

import (
	"net/netip"
	"testing"
	"time"
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

// Handoff mode without the pieces it needs is a usage error.
func TestHandoffNeedsDirectAndRelay(t *testing.T) {
	for _, cfg := range []Config{{Handoff: true, Relay: &RelayConfig{}}, {Handoff: true, Direct: &DirectConfig{HopInterval: time.Second}}} {
		cfg.Name = "a"
		if _, err := Run(cfg); err == nil {
			t.Errorf("%+v: no error", cfg)
		}
	}
}
