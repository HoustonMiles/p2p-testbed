package noise

import "testing"

// These run on every datagram the hopper receives from the peer's IP,
// i.e. on attack traffic: none may panic, and nothing forged may pass.
//
//	go test -fuzz=FuzzOpen -fuzztime=30s ./internal/noise

// FuzzOpen: a datagram that was not sealed by the peer never authenticates.
func FuzzOpen(f *testing.F) {
	a, b := handshake(f)
	real, _ := a.Seal(nil, typeData, []byte("hello"))
	f.Add(real)
	f.Add([]byte{typeData})
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
	for _, pattern := range []string{"kk", "ik", "xx"} {
		a, _ := sides(f, pattern, testKey(2).Public, testKey(1).Public)
		first, _ := a.Start()
		f.Add(pattern == "xx", first)
	}
	f.Add(false, []byte{})
	f.Fuzz(func(t *testing.T, xx bool, msg []byte) {
		// The responder expects key 3, which nobody here holds, so no
		// message may complete the handshake.
		pattern := map[bool]string{false: "kk", true: "xx"}[xx]
		_, b := sides(t, pattern, testKey(2).Public, testKey(3).Public)
		if _, err := b.Handle(msg); b.Done() || (pattern == "kk" && err == nil) {
			t.Fatalf("%s handshake accepted %x", pattern, msg)
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
