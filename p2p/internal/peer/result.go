package peer

import (
	"sort"
	"time"

	"p2ptestbed/p2p/internal/direct"
)

// Result is one row of the tool's JSON output: one link, with the peer's
// own measurements repeated in every row so each row stands alone.
// Pointers are null when a step did not happen, so every trial has the
// same columns.
type Result struct {
	PeerResult
	LinkResult
}

// PeerResult is what this peer measured once, for all its links.
type PeerResult struct {
	Name    string `json:"name"`
	Room    uint64 `json:"room"`
	ID      string `json:"id"`      // our peer ID, hex
	Members int    `json:"members"` // members of the room we met, us included
	Local   string `json:"local"`

	// stun_addr and rdv_addr are the same socket seen by two servers; they
	// differ behind an endpoint-dependent ("symmetric") NAT.
	STUNAddr      *string  `json:"stun_addr"`
	STUNMs        *float64 `json:"stun_ms"`
	RdvAddr       *string  `json:"rdv_addr"`
	MappingVaries *bool    `json:"mapping_varies"`
	KeepsPort     *bool    `json:"keeps_port"` // STUN-mapped port == local port (direct mode)

	RateMbit      float64  `json:"rate_mbit"`
	PacketBytes   int      `json:"packet_bytes"`
	Direct        bool     `json:"direct"`
	Noise         string   `json:"noise"`
	PeerKeyFrom   string   `json:"peer_key_from"`
	HopIntervalMs *float64 `json:"hop_interval_ms"`
	ClockOffsetMs float64  `json:"clock_offset_ms"`
	ClockDriftPPM float64  `json:"clock_drift_ppm"`

	Relayed   bool     `json:"relayed"`    // every link went through the TURN relay
	Handoff   bool     `json:"handoff"`    // each link hopped where it could, else went through the relay
	RelayAddr *string  `json:"relay_addr"` // our relayed address
	RelayMs   *float64 `json:"relay_ms"`   // start to the allocation

	StartUnixMs float64 `json:"start_unix_ms"` // when the run began: puts every member's *_ms on one timeline
	TotalMs     float64 `json:"total_ms"`
}

// LinkResult is what this peer measured on one link.
type LinkResult struct {
	PeerID string `json:"peer_id"` // the member at the other end, hex
	Error  string `json:"error"`

	RendezvousMs    *float64 `json:"rendezvous_ms"` // start to this member's introduction
	PeerRdvAddr     *string  `json:"peer_rdv_addr"`
	PeerAddr        *string  `json:"peer_addr"`         // the address that worked
	PeerAddrChanged *bool    `json:"peer_addr_changed"` // learned from its packets (peer-reflexive)
	Punched         bool     `json:"punched"`
	PunchMs         *float64 `json:"punch_ms"` // start of punching to first PunchAck
	PunchesSent     int      `json:"punches_sent"`
	PunchesRecv     int      `json:"punches_recv"`
	ReadyMs         *float64 `json:"ready_ms"`        // start to the link being usable: punched (relayed: channel bound) and connected; in direct mode also hopping both ways (relayed: the handshake done; handoff: on its path)
	Path            string   `json:"path"`            // what carried the link once usable: punch (raw UDP), hop or relay
	HandoffReason   string   `json:"handoff_reason"`  // handoff mode, on the relay: punch (failed), ports (a NAT changes them) or hop (the first hop failed)
	Pivoted         bool     `json:"pivoted"`         // handoff mode: the hopping link moved to the relay while it ran
	PivotMs         *float64 `json:"pivot_ms"`        // since start, when this side moved it
	PivotReason     string   `json:"pivot_reason"`    // missed (hops in a row) or member (its traffic came through the relay)
	PeerRelayAddr   *string  `json:"peer_relay_addr"` // relayed mode: the member's relayed address, where our traffic goes
	RelayBindMs     *float64 `json:"relay_bind_ms"`   // relayed mode: time to bind our channel to it

	PingsSent int       `json:"pings_sent"`
	PingsRecv int       `json:"pings_recv"`
	PingLoss  *float64  `json:"ping_loss"`
	RTTMean   *float64  `json:"rtt_ms_mean"`
	RTTP50    *float64  `json:"rtt_ms_p50"`
	RTTP95    *float64  `json:"rtt_ms_p95"`
	RTTMin    *float64  `json:"rtt_ms_min"`
	RTTMax    *float64  `json:"rtt_ms_max"`
	RTTStdev  *float64  `json:"rtt_ms_stdev"`
	RTTs      []float64 `json:"rtt_ms_samples"` // in ping order; lost pings omitted

	// tx_* is what this side sent on the link; rx_* what arrived here, so
	// a's rx_* is the b->a direction.
	TxPackets  *int64   `json:"tx_packets"`
	TxMbit     *float64 `json:"tx_mbit"`
	RxPackets  *int64   `json:"rx_packets"`
	RxPeerSent *int64   `json:"rx_peer_sent"`
	RxLoss     *float64 `json:"rx_loss"`
	RxMbit     *float64 `json:"rx_mbit"`     // payload rate while packets arrived
	RxIPMbit   *float64 `json:"rx_ip_mbit"`  // the same, counting IP, UDP and Noise headers
	RxStartMs  *float64 `json:"rx_start_ms"` // first and last Data packet, since start: when the test ran
	RxEndMs    *float64 `json:"rx_end_ms"`

	Initiator     *bool    `json:"initiator"` // this side started the handshake (the lower static key)
	HandshakeMs   *float64 `json:"handshake_ms"`
	PeerKeepsPort *bool    `json:"peer_keeps_port"`
	HopPossible   *bool    `json:"hop_possible"`
	Hopping       bool     `json:"hopping"`
	HopConnected  bool     `json:"hop_connected"` // the first hop worked both ways
	direct.Stats           // embedded: flat hop_* columns
}

// rows builds the output, sorted by peer ID. The run's error, if any, goes
// into every row that has none of its own.
func (r *runner) rows(err error) []*Result {
	if len(r.links) == 0 {
		return []*Result{{PeerResult: r.res, LinkResult: LinkResult{Error: errText(err)}}}
	}
	var rows []*Result
	for _, l := range r.sortedLinks() {
		row := &Result{PeerResult: r.res, LinkResult: l.res}
		switch {
		case l.hopStats != nil:
			row.Stats = *l.hopStats
		case l.hopper != nil:
			row.Stats = l.hopper.Stats() // run has closed it, so these are final
		}
		if row.Error == "" {
			row.Error = errText(err)
		}
		rows = append(rows, row)
	}
	return rows
}

func (r *runner) sortedLinks() []*link {
	ls := make([]*link, 0, len(r.links))
	for _, l := range r.links {
		ls = append(ls, l)
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].peerID < ls[j].peerID })
	return ls
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func ptr[T any](v T) *T { return &v }
