package direct

import (
	"encoding/binary"
	"net"
	"net/netip"
	"time"
)

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
	for port, sk := range h.socks {
		if !used[port] {
			sk.conn.Close() // its reader sees the error and exits
			delete(h.socks, port)
		}
	}
}

// probe is a probe to send once the lock is released.
type probe struct {
	conn  *net.UDPConn
	to    netip.AddrPort
	slot  uint64
	heard bool
}

const probeHeard = 1 // flags bit: the sender has heard the receiver on this slot

// dueProbes picks the slots to probe now: often until the path works both
// ways, then only as a keepalive. Hearing the peer proves only its
// direction, so the peer's probes carry whether it hears us; stopping as
// soon as we hear would leave the peer deaf on that slot until data flows.
func (h *Hopper) dueProbes(now time.Time) []probe {
	var probes []probe
	for slot, st := range h.slots {
		if st.sock == nil {
			continue
		}
		heard := st.sock.heard.Load()
		every := h.cfg.ProbeInterval
		if heard && st.peerHeard {
			every = h.cfg.KeepAlive
		}
		if now.Sub(st.lastProbe) >= every {
			st.lastProbe = now
			probes = append(probes, probe{st.sock.conn, netip.AddrPortFrom(h.cfg.PeerIP, st.peerPort), slot, heard})
		}
	}
	return probes
}

func (h *Hopper) sendProbe(p probe) {
	var flags byte
	if p.heard {
		flags = probeHeard
	}
	body := append(binary.BigEndian.AppendUint64(make([]byte, 0, 9), p.slot), flags)
	pkt, err := h.cfg.Session.Seal(nil, TypeProbe, body)
	if err != nil {
		return
	}
	if _, err := p.conn.WriteToUDPAddrPort(pkt, p.to); err == nil {
		h.stats.probesSent.Add(1)
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

