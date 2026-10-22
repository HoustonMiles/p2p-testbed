package direct

import (
	"bytes"
	"net/netip"
	"p2ptestbed/p2p/internal/identity"
	"p2ptestbed/p2p/internal/noise"
	"sync/atomic"
	"testing"
	"time"
)

// TestHopperLoopback runs two hoppers against each other on 127.0.0.1 with
// short slots and checks that traffic keeps flowing across several hops.
//
// "peer behind" is the case a fixed overlap gets wrong: b's clock is 60 ms
// behind, so b keeps sending to a's old port for 60 ms after a's boundary,
// longer than MinOverlap (40 ms). To a this looks just like packets still
// queued in a router. The old port must stay open until they have arrived.
func TestHopperLoopback(t *testing.T) {
	const ms = time.Millisecond
	for _, c := range []struct {
		name                     string
		offset                   time.Duration // b's clock relative to a's
		interval, lead, min, max time.Duration
		drainA                   [2]time.Duration // a's mean drain time must be in this range
		cutoffs                  bool             // expect a to cut off busy old ports (and lose packets)
	}{
		{"in sync", 0, 250 * ms, 50 * ms, 40 * ms, 150 * ms, [2]time.Duration{40 * ms, 100 * ms}, false},
		{"peer ahead 20ms", 20 * ms, 250 * ms, 50 * ms, 40 * ms, 150 * ms, [2]time.Duration{40 * ms, 100 * ms}, false},
		{"peer behind 60ms", -60 * ms, 300 * ms, 80 * ms, 40 * ms, 150 * ms, [2]time.Duration{60 * ms, 150 * ms}, false},
		// MaxOverlap (50 ms) shorter than the 60 ms b keeps sending to a's
		// old port: a must cut the old port off while busy, and say so. The
		// upper bound allows for housekeeping running late under load.
		{"peer behind past max", -60 * ms, 300 * ms, 80 * ms, 40 * ms, 50 * ms, [2]time.Duration{40 * ms, 90 * ms}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			sa, sb := hopperRun(t, c.offset, c.interval, c.lead, c.min, c.max, c.cutoffs)
			got := time.Duration(sa.DrainMsAvg * float64(ms))
			if got < c.drainA[0] || got > c.drainA[1] {
				t.Errorf("a: old ports stayed open %v on average, want %v to %v", got, c.drainA[0], c.drainA[1])
			}
			if sb.DrainMsAvg == 0 {
				t.Error("b: no drain recorded")
			}
			if c.cutoffs && sa.DrainCutoffs < 3 {
				t.Errorf("a: %d cutoffs, want one per hop (at least 3)", sa.DrainCutoffs)
			}
			if !c.cutoffs && (sa.DrainCutoffs > 0 || sb.DrainCutoffs > 0) {
				t.Errorf("cutoffs a %d, b %d; want none", sa.DrainCutoffs, sb.DrainCutoffs)
			}
		})
	}
}

// OnHop hears of every hop as it ends: all ok while both peers hop (the
// probes alone keep an idle link heard), then missed once b is gone.
func TestOnHopReportsEachHop(t *testing.T) {
	a, b := sessions(t)
	lo := netip.MustParseAddr("127.0.0.1")
	var ok, missed atomic.Int64
	mk := func(sess *noise.Session, onHop func(bool)) *Hopper {
		h, err := NewHopper(Config{Session: sess, Schedule: NewSchedule(sess.HopKey(), 100*time.Millisecond),
			PeerIP: lo, LocalIP: lo, OnHop: onHop})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	ha := mk(a, func(heard bool) {
		if heard {
			ok.Add(1)
		} else {
			missed.Add(1)
		}
	})
	defer ha.Close()
	hb := mk(b, nil)
	time.Sleep(600 * time.Millisecond)
	hb.Close()
	okBefore, missedBefore := ok.Load(), missed.Load()
	time.Sleep(500 * time.Millisecond)
	ha.Close() // no OnHop call after this, so the counts are final
	st := ha.Stats()
	if okBefore < 3 || missedBefore != 0 {
		t.Errorf("while b hopped: %d ok, %d missed; want several ok, none missed", okBefore, missedBefore)
	}
	if missed.Load() < 3 || uint64(ok.Load()) != st.HopsOK || uint64(missed.Load()) != st.HopsMissed {
		t.Errorf("after b left: OnHop %d ok, %d missed; Stats %d ok, %d missed", ok.Load(), missed.Load(), st.HopsOK, st.HopsMissed)
	}
}

// hopperRun runs both hoppers and checks every packet arrived, unless lossy.
func hopperRun(t *testing.T, offset, interval, lead, minOverlap, maxOverlap time.Duration, lossy bool) (Stats, Stats) {
	a, b := sessions(t)
	lo := netip.MustParseAddr("127.0.0.1")
	var gotA, gotB atomic.Uint64

	mk := func(sess *noise.Session, got *atomic.Uint64, clock func() time.Time) *Hopper {
		s := NewSchedule(sess.HopKey(), interval)
		s.Clock = clock
		h, err := NewHopper(Config{
			Session: sess, Schedule: s, PeerIP: lo, LocalIP: lo,
			Lead: lead, MinOverlap: minOverlap, MaxOverlap: maxOverlap,
			OnPacket: func(Inbound) { got.Add(1) },
			Logf:     t.Logf,
		})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	ha := mk(a, &gotA, nil)
	hb := mk(b, &gotB, SkewedClock(offset, 0))
	defer ha.Close()
	defer hb.Close()

	// Let the first probes cross, then send both ways for several slots.
	time.Sleep(50 * time.Millisecond)
	deadline := time.Now().Add(6 * interval)
	for time.Now().Before(deadline) {
		ha.Send([]byte("from a"))
		hb.Send([]byte("from b"))
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)

	sa, sb := ha.Stats(), hb.Stats()
	t.Logf("a: %+v", sa)
	t.Logf("b: %+v", sb)
	for _, c := range []struct {
		name     string
		sent     uint64
		received uint64
		hops     Stats
	}{{"a->b", sa.Sent, gotB.Load(), sb}, {"b->a", sb.Sent, gotA.Load(), sa}} {
		if c.sent == 0 || !lossy && float64(c.received) < 0.99*float64(c.sent) {
			t.Errorf("%s: %d of %d arrived", c.name, c.received, c.sent)
		}
		if c.hops.HopsOK < 4 || c.hops.HopsMissed > 0 {
			t.Errorf("%s: hops ok %d, missed %d", c.name, c.hops.HopsOK, c.hops.HopsMissed)
		}
	}
	return sa, sb
}

// sessions runs a KK handshake between two fixed keys.
func sessions(t testing.TB) (a, b *noise.Session) {
	t.Helper()
	keys := [2]identity.Keypair{}
	for i := range keys {
		var err error
		if keys[i], err = identity.FromPrivate(bytes.Repeat([]byte{byte(i + 1)}, identity.KeySize)); err != nil {
			t.Fatal(err)
		}
	}
	ha, _ := noise.NewHandshake(noise.HandshakeConfig{Pattern: "kk", Initiator: true, Static: keys[0], PeerKey: keys[1].Public})
	hb, _ := noise.NewHandshake(noise.HandshakeConfig{Pattern: "kk", Static: keys[1], PeerKey: keys[0].Public})
	msg, _ := ha.Start()
	for to, from := hb, ha; msg != nil; to, from = from, to {
		msg, _ = to.Handle(msg)
	}
	if !ha.Done() || !hb.Done() {
		t.Fatal("handshake did not finish")
	}
	return ha.Session(), hb.Session()
}
