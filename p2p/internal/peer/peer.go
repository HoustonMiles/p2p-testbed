// Package peer is one side of a peer-to-peer trial. Both peers run the same
// steps at the same time, meeting at a barrier between steps:
//
//  1. STUN        our public address, as one server sees it (reported only)
//  2. Rendezvous  register in the room until the server introduces a peer
//  3. Punch       punch until a PunchAck proves the path works both ways
//  4. Ping        both peers ping each other
//  5. Throughput  the peer with the lower ID sends at RateMbit for Duration,
//     then the other
//
// With Config.Direct set, steps 4 and 5 run over the Port-Hopping Direct
// Channel (package direct) instead of plain UDP: after the punch the peers
// run a Noise handshake, check that both NATs keep ports, and start hopping.
// Both modes report the same columns, so they compare directly.
//
// Everything before hopping uses ONE socket, because the NAT mapping the
// rendezvous server sees is the one the peers punch through.
//
// TODO(paper §5, mesh step 3): this runs ONE link, with the first member
// the rendezvous introduces. A link manager will run one per member, keep
// registering while it runs, and drop links on PeerLeft.
//
// TODO(paper §5 Relayed Mode): add a relay mode that runs steps 4 and 5
// through the coturn relay (203.0.113.102) with the same Noise transport.
package peer

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"p2ptestbed/p2p/internal/direct"
	"p2ptestbed/p2p/internal/identity"
	"p2ptestbed/p2p/internal/noise"
	"p2ptestbed/p2p/internal/rendezvous"
	"p2ptestbed/p2p/internal/stun"
	"p2ptestbed/p2p/internal/wire"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	Name       string // label for logs and results, e.g. "a"
	Room       uint64 // shared by the peers that should meet (in the testbed, the trial seed)
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
	Noise            string           // handshake pattern: kk, ik or xx
	Key              identity.Keypair // this peer's static key
	TrustedKeys      [][]byte         // with PeerKeyFrom "file": the keys of the network's members
	PeerKeyFrom      string           // "file", "rendezvous" (take the key it announces) or "none" (trust on first use)
	HandshakeTimeout time.Duration    // 0: PunchTimeout
	HopInterval      time.Duration    // required
	HopLead          time.Duration
	HopMinOverlap    time.Duration
	HopMaxOverlap    time.Duration
	HopDrainIdle     time.Duration
	HopForce         bool // hop even when a NAT changes ports, to measure the failure
	ClockOffset      time.Duration
	ClockDriftPPM    float64

	dropHopSends bool // tests: mimic a NAT that blocks one direction of the hop path
}

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

	id     uint64
	peerID atomic.Uint64 // 0 until introduced; read by the reader goroutines

	rdv       *rendezvous.Client
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

	hs     *noise.Handshake
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
		id:        newID(cfg),
		res: &Result{Name: cfg.Name, Room: cfg.Room,
			RateMbit: cfg.RateMbit, PacketBytes: cfg.Size},
	}
	r.res.ID = idString(r.id)
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
	if r.cfg.Name == "" {
		return errors.New("a peer name is required")
	}
	if r.cfg.Direct != nil && r.cfg.Direct.HopInterval <= 0 {
		return errors.New("direct mode needs a hop interval")
	}
	return nil
}

func (r *runner) logf(format string, args ...any) {
	if r.cfg.Logf != nil {
		r.cfg.Logf("[%s] "+format, append([]any{r.cfg.Name}, args...)...)
	}
}

// newID is this peer's ID on the wire: in direct mode the start of its
// static key, so the ID names the key the handshake proves; random in raw
// mode. Never 0, which marks the rendezvous server.
func newID(cfg Config) uint64 {
	var b [8]byte
	if cfg.Direct != nil && len(cfg.Direct.Key.Public) >= len(b) {
		copy(b[:], cfg.Direct.Key.Public)
	} else {
		rand.Read(b[:])
	}
	return max(binary.BigEndian.Uint64(b[:]), 1)
}

func idString(id uint64) string { return fmt.Sprintf("%016x", id) }
