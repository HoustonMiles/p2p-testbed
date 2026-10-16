package noise

import "testing"

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
