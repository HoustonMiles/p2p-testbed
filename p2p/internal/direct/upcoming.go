package direct

import "time"

// SlotPort is one entry of a hopper's upcoming schedule: when a slot starts
// (Unix ms) and the local port it binds. Under a NAT that keeps the local
// port as the public port, this is also the public port the peer receives
// on, which is what the testbed's reactive attacker (an oracle that is told
// the port but reacts with a delay) replays.
type SlotPort struct {
	UnixMs int64
	Port   uint16
}

// Upcoming lists the slots that start within d of now, each with the local
// port this hopper uses for it.
func (h *Hopper) Upcoming(d time.Duration) []SlotPort {
	s := h.cfg.Schedule
	now := s.Now()
	out := make([]SlotPort, 0, int(d/s.Interval)+2)
	for slot := s.SlotAt(now); s.SlotStart(slot).Before(now.Add(d)); slot++ {
		out = append(out, SlotPort{s.SlotStart(slot).UnixMilli(), s.Port(h.side, slot)})
	}
	return out
}
