package direct

import (
	"testing"
	"time"
)

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
