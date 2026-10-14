package direct

import (
	"errors"
	"net/netip"
	"time"
)

// Config for a Hopper. Zero durations take the defaults noted.
type Config struct {
	Session  *Session
	Schedule *Schedule // built from Session.HopKey(); the same on both peers
	Role     byte      // 'a' or 'b'; the peer is the other
	PeerIP   netip.Addr
	LocalIP  netip.Addr // zero binds all interfaces

	Lead time.Duration // open the next slot's port this early; default Interval/10

	// When the previous slot's port closes; see drainCheck.decide.
	// MinOverlap (default Interval/10) is also the clock tolerance on an idle
	// link. MaxOverlap defaults to Interval/2. DrainIdle (default 50 ms)
	// covers reordering by jitter.
	MinOverlap time.Duration
	MaxOverlap time.Duration
	DrainIdle  time.Duration

	ProbeInterval time.Duration // default 20 ms
	KeepAlive     time.Duration // default 10 s, under the NAT's 30 s UDP timeout
	Tick          time.Duration // housekeeping period; default 5 ms

	// OnPacket gets every authenticated Data packet, on the reader goroutine
	// of its socket: it must be safe for concurrent use and fast. Payload is
	// only valid during the call.
	OnPacket func(Inbound)

	Logf func(format string, args ...any)
}

// withDefaults fills zero fields.
func (c Config) withDefaults() Config {
	setDefault(&c.Tick, 5*time.Millisecond)
	setDefault(&c.Lead, max(c.Schedule.Interval/10, 2*c.Tick))
	setDefault(&c.MinOverlap, c.Schedule.Interval/10)
	setDefault(&c.MaxOverlap, c.Schedule.Interval/2)
	setDefault(&c.DrainIdle, 50*time.Millisecond)
	setDefault(&c.ProbeInterval, 20*time.Millisecond)
	setDefault(&c.KeepAlive, 10*time.Second)
	c.PeerIP = c.PeerIP.Unmap()
	return c
}

func setDefault(d *time.Duration, v time.Duration) {
	if *d == 0 {
		*d = v
	}
}

func (c Config) validate() error {
	switch {
	case c.Session == nil || c.Schedule == nil:
		return errors.New("direct: Session and Schedule are required")
	case c.Role != 'a' && c.Role != 'b':
		return errors.New("direct: role must be a or b")
	case !c.PeerIP.IsValid():
		return errors.New("direct: PeerIP is required")
	}
	return c.validateTiming()
}

func (c Config) validateTiming() error {
	switch {
	case c.Lead < c.Tick:
		// Housekeeping only runs every Tick, so a shorter Lead could open
		// the next port after its slot has begun.
		return errors.New("direct: Lead must be at least Tick")
	case c.MinOverlap > c.MaxOverlap:
		return errors.New("direct: MinOverlap must not exceed MaxOverlap")
	case c.Lead+c.MaxOverlap >= c.Schedule.Interval:
		return errors.New("direct: Lead + MaxOverlap must be shorter than the slot Interval")
	}
	return nil
}

