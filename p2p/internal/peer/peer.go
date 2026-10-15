// Package peer is one side of a peer-to-peer trial. Both peers run the same
// steps at the same time, meeting at a barrier between steps:
//
//  1. STUN        our public address, as one server sees it (reported only)
//  2. Rendezvous  register until the server introduces the other peer
//  3. Punch       punch until a PunchAck proves the path works both ways
//  4. Ping        both peers ping each other
//  5. Throughput  a sends to b at RateMbit for Duration, then b to a
//
// With Config.Direct set, steps 4 and 5 run over the Port-Hopping Direct
// Channel (package direct) instead of plain UDP: after the punch the peers
// run a Noise handshake, check that both NATs keep ports, and start hopping.
// Both modes report the same columns, so they compare directly.
//
// Everything before hopping uses ONE socket, because the NAT mapping the
// rendezvous server sees is the one the peers punch through.
//
// TODO(paper §5 Relayed Mode): add a relay mode that runs steps 4 and 5
// through the coturn relay (203.0.113.102) with the same Noise transport.
package peer

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"

	"p2ptestbed/p2p/internal/direct"
	"p2ptestbed/p2p/internal/stun"
	"p2ptestbed/p2p/internal/wire"
)

type Config struct {
	Role       byte   // 'a' or 'b'
	Session    uint64 // shared by both peers of one trial
	Local      string // local UDP address to bind, e.g. ":0"
	STUN       string // host:port; "" skips step 1
	Rendezvous string

	STUNTimeout       time.Duration
	RendezvousTimeout time.Duration
	PunchTimeout      time.Duration
	PunchInterval     time.Duration
	StepTimeout       time.Duration // how long to wait for the peer at a barrier

	Pings        int // 0 skips step 4
	PingInterval time.Duration
	PingGrace    time.Duration // wait for late replies after the last ping

	RateMbit float64       // offered load per direction; 0 skips step 5
	Duration time.Duration // per direction
	Size     int           // UDP payload bytes per Data packet
	Drain    time.Duration // wait for queued packets after each direction

	Linger time.Duration // keep answering the peer this long before exiting

	Direct *DirectConfig // nil: raw UDP

	Logf func(format string, args ...any)
}

// DirectConfig sets up the Port-Hopping Direct Channel. Zero durations take
// package direct's defaults.
type DirectConfig struct {
	Noise            string         // handshake pattern: kk, ik or xx
	Key              direct.Keypair // this peer's static key
	PeerKey          []byte         // the peer's expected static key, with PeerKeyFrom "file"
	PeerKeyFrom      string         // "file", "rendezvous" (take the key it announces) or "none" (trust on first use)
	HandshakeTimeout time.Duration  // 0: PunchTimeout
	HopInterval      time.Duration  // required
	HopLead          time.Duration
	HopMinOverlap    time.Duration
	HopMaxOverlap    time.Duration
	HopDrainIdle     time.Duration
	HopForce         bool // hop even when a NAT changes ports, to measure the failure
	ClockOffset      time.Duration
	ClockDriftPPM    float64

	dropHopSends bool // tests: mimic a NAT that blocks one direction of the hop path
}

// Result is the tool's JSON output. Pointers are null when a step did not
// happen, so every trial has the same columns.
type Result struct {
	Role    string `json:"role"`
	Session uint64 `json:"session"`
	Local   string `json:"local"`
	Error   string `json:"error"`

	// stun_addr and rdv_addr are the same socket seen by two servers; they
	// differ behind an endpoint-dependent ("symmetric") NAT.
	STUNAddr        *string `json:"stun_addr"`
	RdvAddr         *string `json:"rdv_addr"`
	MappingVaries   *bool   `json:"mapping_varies"`
	PeerRdvAddr     *string `json:"peer_rdv_addr"`
	PeerAddr        *string `json:"peer_addr"`         // the address that worked
	PeerAddrChanged *bool   `json:"peer_addr_changed"` // learned from its packets (peer-reflexive)

	STUNMs       *float64 `json:"stun_ms"`
	RendezvousMs *float64 `json:"rendezvous_ms"`
	Punched      bool     `json:"punched"`
	PunchMs      *float64 `json:"punch_ms"` // introduction to first PunchAck
	PunchesSent  int      `json:"punches_sent"`
	PunchesRecv  int      `json:"punches_recv"`

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

	// tx_* is what this side sent; rx_* what arrived here, so a's rx_* is
	// the b->a direction.
	RateMbit    float64  `json:"rate_mbit"`
	PacketBytes int      `json:"packet_bytes"`
	TxPackets   *int64   `json:"tx_packets"`
	TxMbit      *float64 `json:"tx_mbit"`
	RxPackets   *int64   `json:"rx_packets"`
	RxPeerSent  *int64   `json:"rx_peer_sent"`
	RxLoss      *float64 `json:"rx_loss"`
	RxMbit      *float64 `json:"rx_mbit"`    // payload rate while packets arrived
	RxIPMbit    *float64 `json:"rx_ip_mbit"` // the same, counting IP, UDP and Noise headers

	Direct        bool     `json:"direct"`
	Noise         string   `json:"noise"`
	PeerKeyFrom   string   `json:"peer_key_from"`
	HandshakeMs   *float64 `json:"handshake_ms"`
	KeepsPort     *bool    `json:"keeps_port"` // STUN-mapped port == local port
	PeerKeepsPort *bool    `json:"peer_keeps_port"`
	HopPossible   *bool    `json:"hop_possible"`
	Hopping       bool     `json:"hopping"`
	HopConnected  bool     `json:"hop_connected"` // the first hop worked both ways
	HopIntervalMs *float64 `json:"hop_interval_ms"`
	ClockOffsetMs float64  `json:"clock_offset_ms"`
	ClockDriftPPM float64  `json:"clock_drift_ppm"`
	direct.Stats           // embedded: flat hop_* columns

	TotalMs float64 `json:"total_ms"`
}

// Barrier steps must rise in the order they run: a peer answers a Barrier
// for every step up to the one it has reached.
const (
	stepConnected = 1
	stepSecured   = 2
	stepHopping   = 3
	stepPinged    = 4
	stepAtoB      = 5
	stepBtoA      = 6
)

type inbound struct {
	from netip.AddrPort
	at   time.Duration // since start
	pkt  wire.Packet
	stun []byte // set instead of pkt for STUN messages
	hop  bool   // came through the Hopper, so answer through it
}

type runner struct {
	cfg  Config
	conn *net.UDPConn
	in   chan inbound
	t0   time.Time
	res  *Result

	stunTx  stun.TxID
	stunGot netip.AddrPort

	rdv       netip.AddrPort
	rdvKey    []byte // the peer's static key, as the rendezvous announced it
	peerInfo  bool
	peerRdv   netip.AddrPort
	remote    netip.AddrPort // where peer traffic goes; updated by learn
	gotAck    bool
	ackAt     time.Duration
	reached   uint64            // highest barrier step this side has reached
	myValue   map[uint64]uint64 // value attached to each of our steps
	peerValue map[uint64]uint64 // steps the peer has reached, with its value
	acked     map[uint64]bool   // steps the peer answered with a BarrierAck
	rtts      map[uint64]float64

	rxMu sync.Mutex // the reader goroutines count Data here
	rx   rxCounts

	hs     *direct.Handshake
	hopper *direct.Hopper // once set, all peer traffic goes through it
}

// Run performs every step and returns what it measured. A failed punch or
// hop is a result; an error means the run itself broke.
func Run(cfg Config) (*Result, error) {
	r := newRunner(cfg)
	err := r.run()
	if r.hopper != nil {
		r.res.Stats = r.hopper.Stats() // run has closed it, so these are final
	}
	r.res.TotalMs = ms(time.Since(r.t0))
	if err != nil {
		r.res.Error = err.Error()
	}
	return r.res, err
}

func newRunner(cfg Config) *runner {
	r := &runner{
		cfg:       cfg,
		t0:        time.Now(),
		in:        make(chan inbound, 4096),
		myValue:   map[uint64]uint64{},
		peerValue: map[uint64]uint64{},
		acked:     map[uint64]bool{},
		rtts:      map[uint64]float64{},
		res: &Result{Role: string(cfg.Role), Session: cfg.Session,
			RateMbit: cfg.RateMbit, PacketBytes: cfg.Size},
	}
	if d := cfg.Direct; d != nil {
		r.res.Direct, r.res.Noise, r.res.PeerKeyFrom = true, d.Noise, d.PeerKeyFrom
		r.res.HopIntervalMs = ptr(ms(d.HopInterval))
		r.res.ClockOffsetMs, r.res.ClockDriftPPM = ms(d.ClockOffset), d.ClockDriftPPM
	}
	return r
}

func (r *runner) run() error {
	if err := r.validate(); err != nil {
		return err
	}
	if err := r.listen(); err != nil {
		return err
	}
	defer r.conn.Close()
	go r.readLoop()

	connected, err := r.connect()
	if err != nil || !connected {
		return err
	}
	if r.cfg.Direct != nil {
		// Deferred after conn.Close, so it runs first: the Hopper's readers
		// must stop before readLoop closes r.in.
		defer r.closeHopper()
		up, err := r.startDirect()
		if err != nil {
			return err
		}
		if !up {
			r.linger()
			return nil
		}
	}
	if err := r.measure(); err != nil {
		return err
	}
	r.linger()
	return nil
}

func (r *runner) validate() error {
	if r.cfg.Role != 'a' && r.cfg.Role != 'b' {
		return errors.New("role must be a or b")
	}
	if r.cfg.Direct != nil && r.cfg.Direct.HopInterval <= 0 {
		return errors.New("direct mode needs a hop interval")
	}
	return nil
}

func (r *runner) listen() error {
	laddr, err := net.ResolveUDPAddr("udp4", r.cfg.Local)
	if err != nil {
		return err
	}
	if r.conn, err = net.ListenUDP("udp4", laddr); err != nil {
		return err
	}
	r.conn.SetReadBuffer(4 << 20)
	r.conn.SetWriteBuffer(4 << 20)
	r.res.Local = r.conn.LocalAddr().String()
	return nil
}

// linger keeps answering the peer, which may still be finishing a step.
func (r *runner) linger() { r.pump(r.cfg.Linger, 0, nil, never) }

func (r *runner) logf(format string, args ...any) {
	if r.cfg.Logf != nil {
		r.cfg.Logf("[%c] "+format, append([]any{r.cfg.Role}, args...)...)
	}
}
