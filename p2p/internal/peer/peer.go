// Package peer is one member of a peer-to-peer trial. It links with every
// other member of its room (a full mesh) and measures each link:
//
//  1. STUN        our public address, as one server sees it (reported only)
//  2. Rendezvous  register in the room until the other members are introduced
//  3. Punch       punch the member until a PunchAck proves the path works both ways
//  4. Ping        ping the member
//  5. Throughput  one link at a time: the lower ID sends at RateMbit for
//     Duration, then the other
//
// Steps 1 and 2 happen once per peer. A link starts the moment the
// rendezvous introduces its member, so members may arrive at different
// times and the mesh grows as they come. Each link runs on its own
// goroutine and moves to its next step as soon as it and the member are
// ready, meeting the member at a barrier between steps; a slow or dead link
// holds up only itself. A link that fails a step drops out with its result.
//
// With Config.Direct set, steps 4 and 5 run over the Port-Hopping Direct
// Channel (package direct) instead of plain UDP: after the punch each link
// runs a Noise handshake, checks that both NATs keep ports, and starts
// hopping; the links' hoppers share one pool of sockets. Both modes report
// the same columns, one row per link, so they compare directly.
//
// Everything before hopping uses ONE socket, because the NAT mapping the
// rendezvous server sees is the one the peers punch through.
//
// Throughput runs pair by pair in one order every member agrees on (sorted
// by the pair's IDs), so no two members ever wait on each other in a cycle.
//
// With Config.Relay set, every link goes through the TURN relay instead of
// a hole punch (relay.go): the relayed baseline the direct channel is
// compared against, with the same steps and columns. With Config.Handoff
// as well (and Config.Direct), each link hops where hopping holds and uses
// the relay where it does not (handoff.go).
package peer

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"p2ptestbed/p2p/internal/direct"
	"p2ptestbed/p2p/internal/identity"
	"p2ptestbed/p2p/internal/rendezvous"
	"p2ptestbed/p2p/internal/stun"
	"p2ptestbed/p2p/internal/turn"
	"p2ptestbed/p2p/internal/wire"
)

type Config struct {
	Name       string // label for logs and results, e.g. "a"
	Room       uint64 // shared by the peers that should meet (in the testbed, the trial seed)
	Peers      int    // members of the room, us included; 0 means 2
	Local      string // local UDP address to bind, e.g. ":0"
	STUN       string // host:port; "" skips step 1
	Rendezvous string

	STUNTimeout       time.Duration
	RendezvousTimeout time.Duration // wait this long for all members
	RefreshInterval   time.Duration // re-register this often while running; 0 means 1 s (under the rendezvous TTL)
	PunchTimeout      time.Duration
	PunchInterval     time.Duration
	StepTimeout       time.Duration // how long to wait for a member at a barrier

	Pings        int // 0 skips step 4
	PingInterval time.Duration
	PingGrace    time.Duration // wait for late replies after the last ping

	RateMbit float64       // offered load per direction; 0 skips step 5
	Duration time.Duration // per direction
	Size     int           // UDP payload bytes per Data packet
	Drain    time.Duration // wait for queued packets after each direction

	Linger time.Duration // keep answering the members this long before exiting

	Direct *DirectConfig // nil: raw UDP
	Relay  *RelayConfig  // nil: punch a direct path; set: every link goes through the TURN relay
	// Handoff, with Direct and Relay: each link hops where it can and goes
	// through the relay where it cannot (handoff.go).
	Handoff bool

	Logf func(format string, args ...any)

	unreachable  netip.Addr // tests: drop all we send there, like a NAT that never lets that member in
	changesPorts bool       // tests: our NAT changes ports, like a symmetric NAT
	dropBarrier  uint64     // tests: never send our Barrier for this step, only answer the member's, as if every one were lost
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
	PivotAfter       int // handoff mode: move a hopping link to the relay after this many missed hops in a row; 0: 3

	dropHopSends   bool          // tests: mimic a NAT that blocks one direction of the hop path
	blockHopsAfter time.Duration // tests: from this long after start, the hoppers send nothing, as behind a NAT that starts dropping them
}

// RelayConfig is the TURN relay for relayed mode (relay.go).
type RelayConfig struct {
	Server             string // host:port
	Username, Password string
}

type inbound struct {
	from   netip.AddrPort
	at     time.Duration // since start
	pkt    wire.Packet
	stun   []byte         // set instead of pkt for STUN messages
	turn   *turn.Response // set instead of pkt for a reply from the TURN relay
	pivot  string         // set instead of pkt: move the link to the relay, and why (handoff mode)
	sealed bool           // came through the relay, sealed with the link's session
	link   *link          // the member it came from; nil for the servers
	hop    bool           // came through the link's Hopper, so answer through it
}

type runner struct {
	cfg  Config
	conn *net.UDPConn
	in   chan inbound
	t0   time.Time
	id   uint64
	res  PeerResult

	stunTx  stun.TxID
	stunGot netip.AddrPort
	rdv     *rendezvous.Client
	met     bool // the meeting is over; later introductions are ignored

	linksMu sync.RWMutex     // links is read by the reader goroutines
	links   map[uint64]*link // by peer ID
	ports   *direct.Ports    // the links' shared hop sockets, in direct mode
	relay   *relay           // our allocation, in relayed mode
	wg      sync.WaitGroup   // the links' goroutines
	done    chan struct{}    // ends the links' goroutines
	stop    chan struct{}    // ends keepRegistered
}

// Run performs every step and returns one row per link (one row carrying
// the error if no link was made). A failed punch or hop is a result; an
// error means the run itself broke.
func Run(cfg Config) ([]*Result, error) {
	r := newRunner(cfg)
	err := r.run()
	r.res.TotalMs = ms(time.Since(r.t0))
	return r.rows(err), err
}

func newRunner(cfg Config) *runner {
	if cfg.Peers == 0 {
		cfg.Peers = 2
	}
	if cfg.RefreshInterval == 0 {
		cfg.RefreshInterval = time.Second
	}
	r := &runner{
		cfg:   cfg,
		t0:    time.Now(),
		in:    make(chan inbound, 4096),
		id:    newID(cfg),
		links: map[uint64]*link{},
		done:  make(chan struct{}),
		stop:  make(chan struct{}),
	}
	r.res = PeerResult{Name: cfg.Name, Room: cfg.Room, ID: idString(r.id), RateMbit: cfg.RateMbit, PacketBytes: cfg.Size,
		StartUnixMs: float64(r.t0.UnixMicro()) / 1000}
	r.res.Relayed, r.res.Handoff = cfg.Relay != nil && !cfg.Handoff, cfg.Handoff
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
	defer close(r.stop)
	go r.readLoop()

	if r.cfg.Direct != nil {
		defer r.closeHoppers() // after linger has stopped the links
		r.ports = direct.NewPorts(r.localIP())
	}
	if r.cfg.Relay != nil {
		var err error
		if r.relay, err = newRelay(r.cfg.Relay); err != nil {
			return err
		}
		defer r.release()
	}
	if err := r.meet(); err != nil {
		return err
	}
	r.runTests()
	r.linger()
	return nil
}

func (r *runner) validate() error {
	switch {
	case r.cfg.Name == "":
		return errors.New("a peer name is required")
	case r.cfg.Peers < 2:
		return errors.New("a room needs at least 2 peers")
	case r.cfg.Direct != nil && r.cfg.Direct.HopInterval <= 0:
		return errors.New("direct mode needs a hop interval")
	case r.cfg.Handoff && (r.cfg.Direct == nil || r.cfg.Relay == nil):
		return errors.New("handoff mode needs the direct channel and a relay")
	}
	return nil
}

// meet runs steps 1 and 2; the links start as their members are introduced.
func (r *runner) meet() error {
	var err error
	if r.rdv, err = r.rendezvousClient(); err != nil {
		return err
	}
	if r.cfg.STUN != "" {
		if err := r.doSTUN(); err != nil {
			r.logf("stun: %v (continuing without it)", err)
		}
	}
	if r.relay != nil {
		if err := r.allocate(); err != nil {
			return err
		}
	}
	if err := r.meetAtRendezvous(); err != nil {
		return err
	}
	go r.keepRegistered()
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
