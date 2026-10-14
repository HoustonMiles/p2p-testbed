package direct

import (
	"testing"
)

// These run on every datagram the hopper receives from the peer's IP,
// i.e. on attack traffic: none may panic, and nothing forged may pass.
//
//	go test -fuzz=FuzzOpen -fuzztime=30s ./internal/direct

// FuzzOpen: a datagram that was not sealed by the peer never authenticates.
func FuzzOpen(f *testing.F) {
	a, b, err := handshake(f, psk(1), psk(1))
	if err != nil {
		f.Fatal(err)
	}
	real, _ := a.Seal(nil, TypeData, []byte("hello"))
	f.Add(real)
	f.Add([]byte{TypeData})
	f.Add(make([]byte, Overhead))
	f.Fuzz(func(t *testing.T, pkt []byte) {
		if _, _, err := b.Open(nil, pkt); err == nil && string(pkt) != string(real) {
			t.Fatalf("forged packet authenticated: %x", pkt)
		}
	})
}

// FuzzHandshake: garbage handshake messages are rejected without panicking
// or completing the handshake.
func FuzzHandshake(f *testing.F) {
	ha, _ := NewHandshake(true, psk(1), nil)
	first, _ := ha.Start()
	f.Add(first)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, msg []byte) {
		hb, _ := NewHandshake(false, psk(2), nil) // a different PSK: nothing may pass
		if _, err := hb.Handle(msg); err == nil || hb.Done() {
			t.Fatalf("handshake accepted %x", msg)
		}
	})
}

// FuzzReplayWindow checks the ring bitmap against a plain map: a counter is
// accepted exactly when it is new and not older than the window.
func FuzzReplayWindow(f *testing.F) {
	f.Add([]byte{0, 5, 5, 3, 200, 1})
	f.Fuzz(func(t *testing.T, steps []byte) {
		var w replayWindow
		seen := map[uint64]bool{}
		var n, top uint64
		for _, s := range steps {
			n = jump(n, s)
			want := !seen[n] && (len(seen) == 0 || n+windowBits >= top+1)
			if got := w.check(n); got != want {
				t.Fatalf("counter %d (top %d): check = %v, want %v", n, top, got, want)
			}
			if want {
				w.update(n)
				seen[n] = true
				top = max(top, n)
			}
		}
	})
}

// jump turns a fuzz byte into the next counter: mostly near the last one,
// sometimes far ahead, sometimes back past the window.
func jump(n uint64, s byte) uint64 {
	switch {
	case s < 200:
		return n + uint64(s) - min(n, 100)
	case s < 240:
		return n + uint64(s)*50
	default:
		return n - min(n, windowBits+uint64(s))
	}
}
