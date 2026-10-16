package direct

import (
	"bytes"
	"testing"
	"time"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func TestSchedulePorts(t *testing.T) {
	s := NewSchedule(key(7), time.Second)
	other := NewSchedule(key(8), time.Second)
	same, differs := 0, 0
	for slot := uint64(0); slot < 1000; slot++ {
		p := s.Port(SideInitiator, slot)
		if p < 10000 || p > 32767 {
			t.Fatalf("port %d out of range", p)
		}
		if p != s.Port(SideInitiator, slot) {
			t.Fatal("not deterministic")
		}
		if p == s.Port(SideResponder, slot) {
			same++
		}
		if p != other.Port(SideInitiator, slot) {
			differs++
		}
	}
	if same > 5 || differs < 990 {
		t.Fatalf("sides collide %d times, other key differs %d times", same, differs)
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
