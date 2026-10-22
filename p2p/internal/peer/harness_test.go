package peer

import (
	"fmt"
	"net"
	"testing"
	"time"

	"p2ptestbed/p2p/internal/identity"
	"p2ptestbed/p2p/internal/rendezvous"
	"p2ptestbed/p2p/internal/stun"
)

// The loopback tests run a fake STUN server, the real rendezvous server and
// one Run per peer. Each peer has its own loopback address (127.0.0.2, .3,
// ...), so the hop ports of different peers never collide.

// Each peer's static keypair, made once for all tests.
var testKeys = func() map[string]identity.Keypair {
	keys := map[string]identity.Keypair{}
	for _, name := range []string{"a", "b", "c", "d", "x"} { // x belongs to nobody in the trial
		kp, err := identity.NewKeypair()
		if err != nil {
			panic(err)
		}
		keys[name] = kp
	}
	return keys
}()

func other(name string) string { return map[string]string{"a": "b", "b": "a"}[name] }

// directConfig is a direct-mode setup; withKeys gives each peer of a pair
// its own key and, with PeerKeyFrom "file", trusts the other's public key.
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

// trustRoom gives each peer its own key and trusts every other member's.
func trustRoom(names []string) func(*Config) {
	return func(c *Config) {
		c.Direct.Key, c.Direct.TrustedKeys = testKeys[c.Name], nil
		for _, n := range names {
			if n != c.Name {
				c.Direct.TrustedKeys = append(c.Direct.TrustedKeys, testKeys[n].Public)
			}
		}
	}
}

// runPair runs a room of two, a and b, and returns each one's only row.
func runPair(t *testing.T, extra Config, per ...func(*Config)) map[string]*Result {
	t.Helper()
	out := map[string]*Result{}
	for name, rows := range runRoom(t, []string{"a", "b"}, extra, per...) {
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows, want 1", name, len(rows))
		}
		out[name] = rows[0]
	}
	return out
}

// runRoom runs one peer per name at once and returns each one's rows.
// extra fills in the direct-mode fields and any timing it sets; per
// (optional) adjusts one peer's config.
func runRoom(t *testing.T, names []string, extra Config, per ...func(*Config)) map[string][]*Result {
	t.Helper()
	servers := startServers(t, extra)
	running := map[string]<-chan []*Result{}
	for i, name := range names {
		c := servers.config(t, name, i, extra)
		if c.Peers == 0 {
			c.Peers = len(names)
		}
		for _, f := range per {
			f(&c)
		}
		running[name] = startPeer(t, c)
	}
	results := map[string][]*Result{}
	for name, rows := range running {
		results[name] = <-rows
	}
	return results
}

// servers are the fake STUN server and the rendezvous of one test.
type servers struct{ stun, rendezvous string }

func startServers(t *testing.T, extra Config) servers {
	return servers{fakeSTUN(t), startRendezvous(t, extra)}
}

func (s servers) config(t *testing.T, name string, i int, extra Config) Config {
	c := peerConfig(name, i, extra)
	c.STUN, c.Rendezvous, c.Logf = s.stun, s.rendezvous, t.Logf
	return c
}

// startPeer runs one peer in the background; its rows arrive on the channel.
func startPeer(t *testing.T, c Config) <-chan []*Result {
	out := make(chan []*Result, 1)
	go func() {
		rows, err := Run(c)
		if err != nil {
			t.Logf("%s: %v", c.Name, err)
		}
		out <- rows
	}()
	return out
}

// peerConfig is the i-th peer's config: short steps, and what extra sets.
// checkPath: punched (and hop-connected in direct mode), or, relayed,
// through a bound channel to a member we only ever knew by its relayed
// address; in handoff mode, either, as its path column says.
func checkPath(t *testing.T, name string, r *Result) {
	t.Helper()
	if r.Handoff {
		checkHandoffPath(t, name, r)
		return
	}
	if r.Relayed {
		if r.RelayBindMs == nil || r.PeerRelayAddr == nil || show(r.PeerRdvAddr) != *r.PeerRelayAddr || r.Punched {
			t.Errorf("%s: relayed link: bind %s, introduced as %s, relayed address %s, punched %v",
				name, show(r.RelayBindMs), show(r.PeerRdvAddr), show(r.PeerRelayAddr), r.Punched)
		}
		return
	}
	if !r.Punched || r.PunchMs == nil {
		t.Errorf("%s: not punched", name)
	}
	if r.PeerAddrChanged == nil || *r.PeerAddrChanged {
		t.Errorf("%s: peer address changed without a NAT", name)
	}
	if r.Direct && !r.HopConnected {
		t.Errorf("%s: not hop connected", name)
	}
}

// checkHandoffPath: the relay channel is bound on every link, to a member
// introduced by its own address; a hopping link punched and hopped, a
// relayed one says why it is not hopping.
func checkHandoffPath(t *testing.T, name string, r *Result) {
	t.Helper()
	if r.RelayBindMs == nil || r.PeerRelayAddr == nil || show(r.PeerRdvAddr) == *r.PeerRelayAddr {
		t.Errorf("%s: bind %s, introduced as %s, relayed address %s", name, show(r.RelayBindMs), show(r.PeerRdvAddr), show(r.PeerRelayAddr))
	}
	if r.Pivoted {
		t.Errorf("%s: moved to the relay at %s ms (%s) in a clean run", name, show(r.PivotMs), r.PivotReason)
	}
	switch r.Path {
	case "hop":
		if !r.Punched || !r.HopConnected || r.HandoffReason != "" {
			t.Errorf("%s: hopping, but punched %v, hop connected %v, reason %q", name, r.Punched, r.HopConnected, r.HandoffReason)
		}
	case "relay":
		if handoffReasons[r.HandoffReason] == "" || r.HopConnected {
			t.Errorf("%s: on the relay, reason %q, hop connected %v", name, r.HandoffReason, r.HopConnected)
		}
	default:
		t.Errorf("%s: path %q", name, r.Path)
	}
}

func peerConfig(name string, i int, extra Config) Config {
	c := Config{
		Name: name, Room: 42, Peers: extra.Peers, Local: net.IPv4(127, 0, 0, byte(2+i)).String() + ":0",
		STUNTimeout: time.Second, RendezvousTimeout: 5 * time.Second, RefreshInterval: extra.RefreshInterval,
		PunchTimeout: 5 * time.Second, PunchInterval: 20 * time.Millisecond, StepTimeout: 5 * time.Second,
		Pings: 20, PingInterval: 5 * time.Millisecond, PingGrace: 200 * time.Millisecond,
		RateMbit: 5, Duration: 300 * time.Millisecond, Size: 1000, Drain: 200 * time.Millisecond,
		Linger: 100 * time.Millisecond,
	}
	if extra.Direct != nil {
		d := *extra.Direct // each peer gets its own copy to adjust
		c.Direct = &d
	}
	if extra.Relay != nil {
		relay := *extra.Relay
		c.Relay = &relay
	}
	c.Handoff = extra.Handoff
	if extra.StepTimeout != 0 {
		c.StepTimeout = extra.StepTimeout
	}
	if extra.RendezvousTimeout != 0 {
		c.RendezvousTimeout = extra.RendezvousTimeout
	}
	return c
}

func fakeSTUN(t *testing.T) string {
	conn := listen(t)
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := conn.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			if stun.IsMessage(buf[:n]) {
				conn.WriteToUDPAddrPort(stun.Response(stun.TxID(buf[8:20]), from), from)
			}
		}
	}()
	return conn.LocalAddr().String()
}

// startRendezvous runs the real server. Its TTL is five refresh intervals,
// as in the testbed (5 s and 1 s).
func startRendezvous(t *testing.T, extra Config) string {
	ttl := 5 * time.Second
	if extra.RefreshInterval != 0 {
		ttl = 5 * extra.RefreshInterval
	}
	conn := listen(t)
	go (&rendezvous.Server{Conn: conn, TTL: ttl}).Serve()
	return conn.LocalAddr().String()
}

func listen(t *testing.T) *net.UDPConn {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// checkRow checks one link's row of a clean run: punched, pinged, and every
// Data packet arrived at about the offered rate.
func checkRow(t *testing.T, name string, r *Result) {
	t.Helper()
	if r.Error != "" {
		t.Errorf("%s: %s", name, r.Error)
	}
	if r.ReadyMs == nil {
		t.Errorf("%s: never ready", name)
	}
	checkPath(t, name, r)
	if since := time.Since(time.UnixMilli(int64(r.StartUnixMs))); since < 0 || since > time.Minute {
		t.Errorf("%s: start_unix_ms %.0f is not when the run began", name, r.StartUnixMs)
	}
	if r.STUNAddr == nil || *r.STUNAddr != r.Local || r.MappingVaries == nil || *r.MappingVaries {
		t.Errorf("%s: stun %s, local %v", name, show(r.STUNAddr), r.Local)
	}
	if r.PingsRecv != 20 || r.RTTP50 == nil {
		t.Errorf("%s: %d/20 pings", name, r.PingsRecv)
	}
	if r.TxPackets == nil || *r.TxPackets == 0 {
		t.Errorf("%s: sent nothing", name)
	}
	if r.RxPackets == nil || r.RxPeerSent == nil || *r.RxPackets != *r.RxPeerSent || *r.RxLoss != 0 {
		t.Errorf("%s: received %s of %s", name, show(r.RxPackets), show(r.RxPeerSent))
	}
	if r.RxMbit == nil || *r.RxMbit < 3 || *r.RxMbit > 7 {
		t.Errorf("%s: rx rate %s, want about 5 Mbit/s", name, show(r.RxMbit))
	}
}

func checkResults(t *testing.T, results map[string]*Result) {
	t.Helper()
	for name, r := range results {
		checkRow(t, name, r)
	}
	if *results["a"].TxPackets != *results["b"].RxPeerSent {
		t.Error("b did not learn how many packets a sent")
	}
}

// show prints a result column for a failure message: its value, or none.
func show[T any](v *T) string {
	if v == nil {
		return "none"
	}
	return fmt.Sprint(*v)
}
