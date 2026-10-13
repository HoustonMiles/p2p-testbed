package direct

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"time"

	"golang.org/x/crypto/hkdf"
)

// Schedule says which port each peer listens on, and when. Time is cut into
// slots of Interval, counted from Epoch; in each slot each role has its own
// port, derived from the hop key:
//
//	port(role, slot) = PortBase + HKDF-Expand(hopKey, "hop port"|role|slot) mod PortCount
//
// Both peers compute the same table from the same key and their own clocks,
// so nothing about the schedule is sent on the wire. An attacker without the
// key cannot tell which port comes next. This is the same idea as TOTP codes.
//
// The schedule gives the LOCAL port. It is also the public port only behind a
// NAT that keeps the source port (the testbed's cone types: plain
// MASQUERADE). Behind --random-fully ("symmetric") the public port is random
// and hopping cannot work; check the peer's MappingVaries result first.
type Schedule struct {
	Key      []byte        // hop key, Session.HopKey()
	Epoch    time.Time     // slot 0 starts here; zero = Unix epoch
	Interval time.Duration // slot length

	// Ports are drawn from [PortBase, PortBase+PortCount). The default,
	// 10000-32767, sits below Linux's ephemeral range (32768-60999), so the
	// kernel never hands one of our next ports to some other socket.
	PortBase  uint16
	PortCount uint16

	// Clock returns the time on this peer's clock; nil = time.Now. Use
	// SkewedClock to test the schedule against unsynchronized clocks.
	Clock func() time.Time
}

// NewSchedule returns a schedule with the default port range and clock.
func NewSchedule(key []byte, interval time.Duration) *Schedule {
	return &Schedule{Key: key, Interval: interval, PortBase: 10000, PortCount: 22768}
}

// Now is the current time on this peer's (possibly skewed) clock.
func (s *Schedule) Now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Schedule) epoch() time.Time {
	if s.Epoch.IsZero() {
		return time.Unix(0, 0)
	}
	return s.Epoch
}

// SlotAt returns the slot that contains t (0 before the epoch).
func (s *Schedule) SlotAt(t time.Time) uint64 {
	d := t.Sub(s.epoch())
	if d < 0 {
		return 0
	}
	return uint64(d / s.Interval)
}

// SlotStart returns when slot begins, in the same clock as SlotAt.
func (s *Schedule) SlotStart(slot uint64) time.Time {
	return s.epoch().Add(time.Duration(slot) * s.Interval)
}

// Port returns the port role ('a' or 'b') listens on during slot. Each role
// gets its own sequence, so the two peers do not hop in lockstep onto the
// same number.
func (s *Schedule) Port(role byte, slot uint64) uint16 {
	info := make([]byte, 0, 18)
	info = append(info, "hop port"...)
	info = append(info, role)
	info = binary.BigEndian.AppendUint64(info, slot)

	// Expand, not New: the hop key is already a uniform 32-byte secret.
	// 4 output bytes make the modulo bias negligible (< 2^-17).
	var out [4]byte
	io.ReadFull(hkdf.Expand(sha256.New, s.Key, info), out[:])
	return s.PortBase + uint16(binary.BigEndian.Uint32(out[:])%uint32(s.PortCount))
}

// SkewedClock returns a clock that is offset from the real one and runs
// driftPPM parts per million fast (negative: slow), counted from the moment
// SkewedClock is called. For the clock offset/drift experiments: give one
// peer a skewed clock and see when hops start failing.
func SkewedClock(offset time.Duration, driftPPM float64) func() time.Time {
	start := time.Now()
	return func() time.Time {
		now := time.Now()
		drift := time.Duration(float64(now.Sub(start)) * driftPPM / 1e6)
		return now.Add(offset + drift)
	}
}

