package direct

import (
	"bytes"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

// handshake runs both sides in memory and returns the two sessions.
func handshake(t *testing.T, pskA, pskB []byte) (a, b *Session, err error) {
	t.Helper()
	ha, err := NewHandshake(true, pskA, []byte("trial 1"))
	if err != nil {
		t.Fatal(err)
	}
	hb, err := NewHandshake(false, pskB, []byte("trial 1"))
	if err != nil {
		t.Fatal(err)
	}
	m1, err := ha.Start()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := hb.Handle(m1)
	if err != nil {
		return nil, nil, err
	}
	if _, err := ha.Handle(m2); err != nil {
		return nil, nil, err
	}
	return ha.Session(), hb.Session(), nil
}

func psk(b byte) []byte { return bytes.Repeat([]byte{b}, PSKSize) }

func TestHandshakeAndTransport(t *testing.T) {
	a, b, err := handshake(t, psk(1), psk(1))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.HopKey(), b.HopKey()) {
		t.Fatal("hop keys differ")
	}
	for _, dir := range []struct{ from, to *Session }{{a, b}, {b, a}} {
		pkt, _ := dir.from.Seal(nil, TypeData, []byte("hello"))
		typ, got, err := dir.to.Open(nil, pkt)
		if err != nil || typ != TypeData || string(got) != "hello" {
			t.Fatalf("open: %v %d %q", err, typ, got)
		}
		if _, _, err := dir.to.Open(nil, pkt); err != ErrReplay {
			t.Fatalf("replay accepted: %v", err)
		}
	}
}

func TestWrongPSKFails(t *testing.T) {
	if _, _, err := handshake(t, psk(1), psk(2)); err == nil {
		t.Fatal("handshake with different PSKs succeeded")
	}
}

func TestHandshakeRetransmits(t *testing.T) {
	ha, _ := NewHandshake(true, psk(1), nil)
	hb, _ := NewHandshake(false, psk(1), nil)
	m1, _ := ha.Start()
	m2, _ := hb.Handle(m1)
	// Reply lost: the initiator resends m1 and must get the same reply.
	again, err := hb.Handle(ha.Last())
	if err != nil || !bytes.Equal(again, m2) {
		t.Fatalf("responder did not resend its reply: %v", err)
	}
	// A forged message does not break the waiting initiator.
	if _, err := ha.Handle([]byte("garbage garbage garbage garbage garbage garbage")); err == nil {
		t.Fatal("garbage accepted")
	}
	if _, err := ha.Handle(m2); err != nil || !ha.Done() {
		t.Fatalf("initiator: %v", err)
	}
}

func TestTamperAndReorder(t *testing.T) {
	a, b, _ := handshake(t, psk(1), psk(1))
	var pkts [][]byte
	for i := 0; i < 100; i++ {
		p, _ := a.Seal(nil, TypeData, []byte{byte(i)})
		pkts = append(pkts, p)
	}
	bad := append([]byte(nil), pkts[0]...)
	bad[len(bad)-1] ^= 1
	if _, _, err := b.Open(nil, bad); err != ErrAuth {
		t.Fatalf("tampered packet: %v", err)
	}
	bad = append([]byte(nil), pkts[0]...)
	bad[0] = TypeProbe // the type is authenticated too
	if _, _, err := b.Open(nil, bad); err != ErrAuth {
		t.Fatalf("retyped packet: %v", err)
	}
	// Reverse order, inside the window: all accepted exactly once.
	for i := len(pkts) - 1; i >= 0; i-- {
		if _, _, err := b.Open(nil, pkts[i]); err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
	}
}

func TestReplayWindow(t *testing.T) {
	var w replayWindow
	accept := func(n uint64) bool {
		if !w.check(n) {
			return false
		}
		w.update(n)
		return true
	}
	if !accept(5) || accept(5) || !accept(3) || accept(3) {
		t.Fatal("basic")
	}
	if !accept(5000) || accept(5000-windowBits-1) || !accept(5000-windowBits+1) {
		t.Fatal("window edge")
	}
	// After a big jump, ring positions reused by new counters are fresh.
	if !accept(5000+windowBits+3) || !accept(5000+windowBits+2) {
		t.Fatal("after jump")
	}
}

func TestSchedulePorts(t *testing.T) {
	s := NewSchedule(psk(7), time.Second)
	other := NewSchedule(psk(8), time.Second)
	same, differs := 0, 0
	for slot := uint64(0); slot < 1000; slot++ {
		p := s.Port('a', slot)
		if p < 10000 || p > 32767 {
			t.Fatalf("port %d out of range", p)
		}
		if p != s.Port('a', slot) {
			t.Fatal("not deterministic")
		}
		if p == s.Port('b', slot) {
			same++
		}
		if p != other.Port('a', slot) {
			differs++
		}
	}
	if same > 5 || differs < 990 {
		t.Fatalf("roles collide %d times, other key differs %d times", same, differs)
	}
	if s.SlotAt(s.SlotStart(42)) != 42 || s.SlotAt(s.SlotStart(43).Add(-1)) != 42 {
		t.Fatal("slot boundaries")
	}
}

func TestSkewedClock(t *testing.T) {
	c := SkewedClock(time.Hour, 0)
	if d := c().Sub(time.Now()); d < 59*time.Minute || d > 61*time.Minute {
		t.Fatalf("offset %v", d)
	}
}

// TestDrained checks the rule for closing the previous slot's socket.
func TestDrainDecision(t *testing.T) {
	cfg := Config{MinOverlap: 40 * time.Millisecond, MaxOverlap: 150 * time.Millisecond, DrainIdle: 50 * time.Millisecond}
	ms := int64(time.Millisecond)
	const boundary = int64(1_000_000_000)
	sock := func(lastAny, lastData int64) *hopSocket {
		s := &hopSocket{}
		s.lastAny.Store(lastAny)
		s.lastData.Store(lastData)
		return s
	}
	for _, c := range []struct {
		name       string
		pos        time.Duration
		old, cur   *hopSocket
		want       bool
		wantCutoff bool
	}{
		// Traffic moved at the boundary, old port quiet since: close once
		// both MinOverlap and DrainIdle have passed.
		{"moved, before MinOverlap", 30 * time.Millisecond, sock(boundary, boundary), sock(boundary+ms, boundary+ms), false, false},
		{"moved, quiet 60ms", 60 * time.Millisecond, sock(boundary, boundary), sock(boundary+ms, boundary+ms), true, false},
		// Queued packets still arriving on the old port: stay open.
		{"old still busy", 100 * time.Millisecond, sock(boundary+95*ms, boundary+95*ms), sock(boundary+ms, boundary+90*ms), false, false},
		// Nothing on the new port yet (peer behind, or idle link): stay.
		{"not moved", 100 * time.Millisecond, sock(boundary-200*ms, boundary-200*ms), sock(boundary-50*ms, boundary-50*ms), false, false},
		// MaxOverlap always closes. While Data is still arriving, that is
		// a cutoff (packets lost); on an idle old port it is not.
		{"max reached, busy", 150 * time.Millisecond, sock(boundary+149*ms, boundary+149*ms), sock(0, 0), true, true},
		{"max reached, idle", 150 * time.Millisecond, sock(boundary-300*ms, boundary-300*ms), sock(0, 0), true, false},
		// Only Data counts as busy: a recent probe alone is no loss.
		{"max reached, probe only", 150 * time.Millisecond, sock(boundary+149*ms, boundary-300*ms), sock(0, 0), true, false},
		// Bind failed for the old slot, or both slots share one socket.
		{"no old socket", 50 * time.Millisecond, nil, sock(0, 0), true, false},
		{"no old socket at max", 150 * time.Millisecond, nil, sock(0, 0), true, false},
	} {
		now := boundary + int64(c.pos)
		got, cutoff := drainCheck{pos: c.pos, now: now, boundary: boundary, old: c.old, cur: c.cur}.decide(cfg)
		if got != c.want || cutoff != c.wantCutoff {
			t.Errorf("%s: drained = %v, cutoff = %v; want %v, %v", c.name, got, cutoff, c.want, c.wantCutoff)
		}
	}
	shared := sock(boundary+149*ms, boundary+149*ms)
	if got, cutoff := (drainCheck{pos: 150 * time.Millisecond, now: boundary + 150*ms, boundary: boundary, old: shared, cur: shared}).decide(cfg); !got || cutoff {
		t.Error("shared socket: want drained, no cutoff (the socket stays open)")
	}
}

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

// hopperRun runs both hoppers and checks every packet arrived, unless lossy.
func hopperRun(t *testing.T, offset, interval, lead, minOverlap, maxOverlap time.Duration, lossy bool) (Stats, Stats) {
	a, b, err := handshake(t, psk(1), psk(1))
	if err != nil {
		t.Fatal(err)
	}
	lo := netip.MustParseAddr("127.0.0.1")
	var gotA, gotB atomic.Uint64

	mk := func(sess *Session, role byte, got *atomic.Uint64, clock func() time.Time) *Hopper {
		s := NewSchedule(sess.HopKey(), interval)
		s.Clock = clock
		h, err := NewHopper(Config{
			Session: sess, Schedule: s, Role: role, PeerIP: lo, LocalIP: lo,
			Lead: lead, MinOverlap: minOverlap, MaxOverlap: maxOverlap,
			OnPacket: func(Inbound) { got.Add(1) },
			Logf:     t.Logf,
		})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	ha := mk(a, 'a', &gotA, nil)
	hb := mk(b, 'b', &gotB, SkewedClock(offset, 0))
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

