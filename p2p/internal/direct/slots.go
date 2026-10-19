package direct

import "time"

func (h *Hopper) loop() {
	defer h.wg.Done()
	t := time.NewTicker(h.cfg.Tick)
	defer t.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-t.C:
			h.step()
		}
	}
}

// step brings the open sockets in line with the schedule. All times come
// from the schedule's clock, so a skewed clock skews everything alike.
func (h *Hopper) step() {
	now := h.cfg.Schedule.Now()
	h.mu.Lock()
	if h.closed() {
		h.mu.Unlock()
		return
	}
	want := h.wantedSlots(now)
	h.retireSlots(want)
	h.openWanted(want)
	h.closeUnusedSockets()
	probes := h.dueProbes(now)
	h.logCurrent(now)
	h.mu.Unlock()

	// Outside the lock, so a slow write never holds up Send or the readers.
	for _, p := range probes {
		h.sendProbe(p)
	}
}

// wantedSlots returns the slots that should be open now: the current one,
// the next one within Lead of its start, and the previous one while it
// drains.
func (h *Hopper) wantedSlots(now time.Time) map[uint64]bool {
	s := h.cfg.Schedule
	cur := s.SlotAt(now)
	want := map[uint64]bool{cur: true}
	if s.Interval-now.Sub(s.SlotStart(cur)) <= h.cfg.Lead {
		want[cur+1] = true
	}
	if h.stillDraining(cur, now) {
		want[cur-1] = true
	}
	return want
}

// retireSlots drops slots no longer wanted and records whether each hop
// worked, i.e. whether anything from the peer arrived on that slot's port.
func (h *Hopper) retireSlots(want map[uint64]bool) {
	for slot, st := range h.slots {
		if want[slot] {
			continue
		}
		delete(h.slots, slot)
		if slot <= h.first {
			continue // the peers start at different moments; see Stats
		}
		if st.sock != nil && st.sock.heard.Load() {
			h.stats.hopsOK.Add(1)
		} else {
			h.stats.hopsMissed.Add(1)
			h.logf("hop: slot %d: nothing arrived from the peer", slot)
		}
	}
}

func (h *Hopper) openWanted(want map[uint64]bool) {
	for slot := range want {
		if _, ok := h.slots[slot]; !ok {
			h.openSlot(slot)
		}
	}
}

func (h *Hopper) closeUnusedSockets() {
	used := map[uint16]bool{}
	for _, st := range h.slots {
		if st.sock != nil {
			used[st.sock.port] = true
		}
	}
	for port := range h.socks {
		if !used[port] {
			h.ports.release(port)
			delete(h.socks, port)
		}
	}
}

func (h *Hopper) logCurrent(now time.Time) {
	cur := h.cfg.Schedule.SlotAt(now)
	if cur == h.last {
		return
	}
	h.last = cur
	if st := h.slots[cur]; st != nil && st.sock != nil {
		h.logf("hop: slot %d, local port %d, peer port %d", cur, st.sock.port, st.peerPort)
	}
}
