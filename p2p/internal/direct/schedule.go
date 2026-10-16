package direct

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"time"

	"golang.org/x/crypto/hkdf"
)

// Schedule maps time slots to ports. Both peers compute the same table from
// the hop key and their own clocks, so nothing about it is sent:
//
//	port(side, slot) = PortBase + HKDF-Expand(hopKey, "hop port"|side|slot) mod PortCount
//
// It gives the LOCAL port, which is also the public port only behind NATs
// that keep source ports.
//
// TODO(paper §5 Procedures, Symmetric NAT): peers behind a NAT that picks
// its own ports should report each new reflexive port over the channel
// before switching; until then the caller skips hopping for them.
//
// TODO(paper §5 Threat Model, countermeasures): slots have a fixed length,
// so hop timing is regular and an adaptive attacker can learn it. Derive a
// per-slot offset from the hop key ("randomized hop timing").
type Schedule struct {
	Key      []byte
	Epoch    time.Time // slot 0 starts here; zero means the Unix epoch
	Interval time.Duration

	// The default range, 10000-32767, sits below Linux's ephemeral ports
	// (32768-60999), so the kernel never hands one of our future ports to
	// another socket.
	PortBase  uint16
	PortCount uint16

	// Clock is this peer's clock; nil means time.Now. SkewedClock makes one
	// for the clock-offset experiments.
	Clock func() time.Time
}

func NewSchedule(key []byte, interval time.Duration) *Schedule {
	return &Schedule{Key: key, Interval: interval, PortBase: 10000, PortCount: 22768}
}

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

// SlotAt returns the slot containing t (0 before the epoch).
func (s *Schedule) SlotAt(t time.Time) uint64 {
	d := t.Sub(s.epoch())
	if d < 0 {
		return 0
	}
	return uint64(d / s.Interval)
}

func (s *Schedule) SlotStart(slot uint64) time.Time {
	return s.epoch().Add(time.Duration(slot) * s.Interval)
}

// The two ends of a link, named by their handshake roles rather than by
// who the peers are, so every pair in a mesh derives its ports the same way.
const (
	SideInitiator byte = 'i'
	SideResponder byte = 'r'
)

// sides returns our side of the link and the peer's.
func sides(initiator bool) (local, peer byte) {
	if initiator {
		return SideInitiator, SideResponder
	}
	return SideResponder, SideInitiator
}

// Port returns side's port in slot. The side is part of the input so the
// two peers' port sequences are independent.
func (s *Schedule) Port(side byte, slot uint64) uint16 {
	info := make([]byte, 0, 18)
	info = append(info, "hop port"...)
	info = append(info, side)
	info = binary.BigEndian.AppendUint64(info, slot)

	// Expand alone is enough: the hop key is already a uniform secret.
	// Four output bytes keep the modulo bias below 2^-17.
	var out [4]byte
	io.ReadFull(hkdf.Expand(sha256.New, s.Key, info), out[:])
	return s.PortBase + uint16(binary.BigEndian.Uint32(out[:])%uint32(s.PortCount))
}

// SkewedClock returns a clock offset from the real one and running driftPPM
// parts per million fast (negative: slow), counted from now.
//
// TODO(paper §4 Objectives, clock tolerance): the runner cannot sweep
// command parameters yet, so offset/drift sweeps need one experiment file
// per value.
func SkewedClock(offset time.Duration, driftPPM float64) func() time.Time {
	start := time.Now()
	return func() time.Time {
		now := time.Now()
		drift := time.Duration(float64(now.Sub(start)) * driftPPM / 1e6)
		return now.Add(offset + drift)
	}
}
