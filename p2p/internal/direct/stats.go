package direct

import (
	"sync/atomic"
	"time"
)

// Stats are counts since the Hopper started. The hop_ prefix lets the
// struct be embedded in the punch tool's flat result row.
//
// TODO(paper §5 Measures #2): add per-hop pinhole re-establishment time
// (slot opened -> first packet from the peer) and jitter around boundaries.
type Stats struct {
	Sent       uint64 `json:"hop_sent"`
	SendErrors uint64 `json:"hop_send_errors"`
	Received   uint64 `json:"hop_received"`
	ProbesSent uint64 `json:"hop_probes_sent"`
	ProbesRecv uint64 `json:"hop_probes_recv"`

	// Dropped on arrival: Foreign (wrong source IP, before any crypto),
	// AuthFailed (forged or corrupted), Replayed.
	Foreign    uint64 `json:"hop_foreign"`
	AuthFailed uint64 `json:"hop_auth_failed"`
	Replayed   uint64 `json:"hop_replayed"`

	// A hop is OK when something from the peer arrived on our port for that
	// slot: this side's inbound direction (the peer reports the other). The
	// slot running at start is not counted, since the two peers start at
	// different moments.
	HopsOK     uint64 `json:"hop_hops_ok"`
	HopsMissed uint64 `json:"hop_hops_missed"`
	BindFailed uint64 `json:"hop_bind_failed"`

	// Should be 0: Send found its slot unopened because housekeeping fell
	// behind, so the peer may have missed packets on that hop too.
	LateOpens uint64 `json:"hop_late_opens"`

	// DrainMsAvg mixes busy hops (closed once drained) with idle ones (open
	// until MaxOverlap). DrainCutoffs counts old ports closed at MaxOverlap
	// while Data was still arriving: the hops that can lose packets. Floods
	// never count, since they fail authentication.
	DrainMsAvg   float64 `json:"hop_drain_ms_avg"`
	DrainCutoffs uint64  `json:"hop_drain_cutoffs"`
}

// counters mirrors Stats with atomics; readers update it concurrently.
type counters struct {
	sent, sendErrors, received, probesSent, probesRecv atomic.Uint64
	foreign, authFailed, replayed                      atomic.Uint64
	hopsOK, hopsMissed, bindFailed, lateOpens          atomic.Uint64
}

func (c *counters) snapshot() Stats {
	return Stats{
		Sent: c.sent.Load(), SendErrors: c.sendErrors.Load(), Received: c.received.Load(),
		ProbesSent: c.probesSent.Load(), ProbesRecv: c.probesRecv.Load(),
		Foreign: c.foreign.Load(), AuthFailed: c.authFailed.Load(), Replayed: c.replayed.Load(),
		HopsOK: c.hopsOK.Load(), HopsMissed: c.hopsMissed.Load(), BindFailed: c.bindFailed.Load(),
		LateOpens: c.lateOpens.Load(),
	}
}

// Stats returns a snapshot of the counters.
func (h *Hopper) Stats() Stats {
	st := h.stats.snapshot()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.drain.count > 0 {
		st.DrainMsAvg = ms(h.drain.total) / float64(h.drain.count)
	}
	st.DrainCutoffs = h.drain.cutoffs
	return st
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
