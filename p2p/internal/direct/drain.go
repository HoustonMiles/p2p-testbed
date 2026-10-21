package direct

import "time"

// stillDraining decides whether the previous slot stays open, and records
// the drain when it closes. A slot from before we started is never reopened.
func (h *Hopper) stillDraining(cur uint64, now time.Time) bool {
	old, ok := h.slots[cur-1]
	if !ok || cur == 0 {
		return false
	}
	boundary := h.cfg.Schedule.SlotStart(cur)
	d := drainCheck{pos: now.Sub(boundary), now: now.UnixNano(), boundary: boundary.UnixNano(), old: old.sock}
	if st := h.slots[cur]; st != nil {
		d.cur = st.sock
	}
	closeOld, cutoff := d.decide(h.cfg)
	if closeOld && cur-1 > h.first {
		h.drain.record(d.pos, cutoff)
		if cutoff {
			h.logf("hop: slot %d: old port cut off at %v while data was still arriving", cur-1, d.pos)
		}
	}
	return !closeOld
}

// drainCheck is the state the close decision for the previous slot needs.
// now and boundary are UnixNano on the schedule's clock.
type drainCheck struct {
	pos           time.Duration // time since the boundary
	now, boundary int64
	old, cur      *hopSocket
}

// decide says whether the old socket can close, and whether closing it cuts
// off Data still arriving (the one case that loses packets).
//
// Never before MinOverlap: a peer whose clock is behind may still be in the
// old slot. Always at MaxOverlap. In between, once Data has arrived on the
// new port since the boundary and the old port has been quiet for
// DrainIdle: packets share one path and arrive in order apart from jitter,
// so by then the old port's packets have all arrived. A full router queue
// delays both ports alike, so the old one stays open exactly as long as the
// queue needs. With no Data on the new port (an idle link) it stays until
// MaxOverlap.
//
// TODO(paper §5 Threat Model): an idle old port stays open until MaxOverlap,
// giving an attacker who found it more time. Consider closing idle ports at
// MinOverlap; Experiment C should measure the trade-off.
func (d drainCheck) decide(cfg Config) (closeOld, cutoff bool) {
	shared := d.old == nil || d.old == d.cur // bind failed, or both slots drew the same port
	switch {
	case d.pos < cfg.MinOverlap:
		return false, false
	case d.pos >= cfg.MaxOverlap:
		return true, !shared && d.now-d.old.lastData.Load() < int64(cfg.DrainIdle)
	case shared:
		return true, false
	case d.cur == nil:
		return false, false
	}
	moved := d.cur.lastData.Load() >= d.boundary
	quiet := d.now-d.old.lastAny.Load() >= int64(cfg.DrainIdle)
	return moved && quiet, false
}

// drainStats is guarded by Hopper.mu.
type drainStats struct {
	count, cutoffs uint64
	total          time.Duration
}

func (d *drainStats) record(open time.Duration, cutoff bool) {
	d.count++
	d.total += open
	if cutoff {
		d.cutoffs++
	}
}
