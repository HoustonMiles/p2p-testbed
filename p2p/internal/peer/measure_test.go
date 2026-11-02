package peer

import (
	"slices"
	"testing"
	"time"
)

func TestSeries(t *testing.T) {
	s := series{every: 50 * time.Millisecond}
	s.add(120*time.Millisecond, 625) // interval 2: 625 bytes in 50 ms is 0.1 Mbit/s
	s.add(260*time.Millisecond, 1250)
	s.add(99*time.Millisecond, 625) // interval 1, counted late by another reader
	start, mbit := s.result()
	if start == nil || *start != 50 {
		t.Errorf("starts at %s ms, want 50", show(start))
	}
	// Intervals 1 to 5; 3 and 4 had nothing.
	if want := []float64{0.1, 0.1, 0, 0, 0.2}; !slices.Equal(mbit, want) {
		t.Errorf("series %v, want %v", mbit, want)
	}
}

func TestSeriesOff(t *testing.T) {
	var s series
	s.add(time.Second, 1000)
	if start, mbit := s.result(); start != nil || mbit != nil {
		t.Errorf("off, but got %s, %v", show(start), mbit)
	}
	s = series{every: 50 * time.Millisecond}
	if start, mbit := s.result(); start != nil || mbit != nil {
		t.Errorf("nothing counted, but got %s, %v", show(start), mbit)
	}
}

// With no interval set, the rows carry no series.
func TestLoopbackSeriesOff(t *testing.T) {
	for name, r := range runPair(t, Config{SampleInterval: -1}) {
		if r.SeriesMs != nil || r.TxSeriesMbit != nil || r.RxSeriesMbit != nil || r.RxSeriesStartMs != nil {
			t.Errorf("%s: series without an interval", name)
		}
		if r.RxPackets == nil || *r.RxPackets == 0 {
			t.Errorf("%s: no throughput test", name)
		}
	}
}
