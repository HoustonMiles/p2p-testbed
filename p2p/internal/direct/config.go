package direct

import (
	"errors"
	"net/netip"
	"p2ptestbed/p2p/internal/noise"
	"time"
)

// Config for a Hopper. Zero durations take the defaults noted.
type Config struct {
	Session  *noise.Session
	Schedule *Schedule // built from Session.HopKey(); the same on both peers
	PeerIP   netip.Addr

	// Ports is shared by all the hoppers of one peer (one per link). Nil
	// gives this hopper sockets of its own, bound to LocalIP (zero: all
	// interfaces).
	Ports   *Ports
	LocalIP netip.Addr

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

	// OnPacket gets every authenticated Data packet, on the Ports reader of
	// its socket, which other links' packets share: it must be safe for
	// concurrent use and fast. Payload is only valid during the call.
	OnPacket func(Inbound)

	// OnHop, if set, hears of each hop as its slot is retired: ok if
	// something from the peer arrived on our port for that slot (the
	// HopsOK and HopsMissed counters). It runs on the hopper's own
	// goroutine, without the hopper's lock, and must not block.
	OnHop func(ok bool)

	// Blocked, if set, is fault injection: while it returns true the
	// hopper sends nothing (Data or probes), as if a NAT dropped all of it.
	Blocked func() bool

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
