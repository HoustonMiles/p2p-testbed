// Package direct is the Port-Hopping Direct Channel: a port schedule
// derived from the session's hop key, and the Hopper that moves the
// channel's sockets along it. Packets are sealed by a noise.Session.
package direct

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"p2ptestbed/p2p/internal/noise"
	"sync"
	"sync/atomic"
	"time"
)

// Packet types. The type travels in the clear but is authenticated as part
// of the associated data.
const (
	TypeData  byte = 1
	TypeProbe byte = 2 // body: slot (u64), flags (u8)
)

// Hopper moves the channel from port to port on the Schedule. Around the
// boundary of slot s:
//
//	   s-1 socket open                 s socket open
//	|----------------------|.....|..........|------------------|
//	                    Lead  ^   drain
//	                 boundary of slot s
//
// Lead before s, both peers bind their port for s and probe each other's,
// which opens both NATs' pinholes (the hole punch, repeated every hop). At
// the boundary Send moves to the new ports. The old socket stays open until
// what the peer sent to it has arrived (see drainCheck.decide).
//
// Config.OnHop reports each hop as it ends, so the Handoff Manager can move
// a link to the relay while it runs.
//
// TODO(paper §5 Dynamic Handoff Mode): report drain cutoffs as they happen
// too, if they turn out to signal a flood before hops are missed.
//
// TODO(paper §5 Threat Model, attacker position): the schedule is secret
// only from off-path attackers. Ports travel in clear UDP headers, so an
// on-path observer can follow each hop about one packet after it happens.
// Experiment C tests both positions.
//
// TODO(paper §5 Measures #7-8): log each slot's start time and ports as
// ground truth, so attacker prediction accuracy and time to retarget can be
// scored against it.
type Hopper struct {
	cfg            Config
	side, peerSide byte // from the handshake; see Schedule.Port

	ports    *Ports
	ownPorts bool // Ports made for this hopper alone, closed with it

	// live is held for reading while a packet is handled, so Close can
	// wait until no OnPacket call is in flight.
	live sync.RWMutex

	mu    sync.Mutex
	socks map[uint16]*hopSocket // our view of the pool's sockets, by local port; consecutive slots may share one
	slots map[uint64]*slotState // slots in their open window
	last  uint64                // last slot logged as current
	first uint64                // the slot running at start; never counted as a hop
	drain drainStats

	stats counters
	done  chan struct{}
	wg    sync.WaitGroup
	once  sync.Once
}

// Inbound is one received Data packet.
type Inbound struct {
	Payload []byte
	From    netip.AddrPort
	Port    uint16    // our local port it arrived on
	At      time.Time // real, not skewed, receive time
}

// hopSocket is what this hopper knows about one shared socket.
type hopSocket struct {
	conn  *net.UDPConn
	port  uint16
	heard atomic.Bool

	// UnixNano on the schedule's clock, so they compare with slot
	// boundaries even when that clock is skewed.
	lastAny, lastData atomic.Int64
}

type slotState struct {
	sock      *hopSocket // nil if the bind failed
	peerPort  uint16
	lastProbe time.Time
	peerHeard bool // the peer's probes say it hears us on this slot
}

var (
	ErrNoSocket = errors.New("direct: no socket for the current slot")
	ErrBlocked  = errors.New("direct: sending blocked (Config.Blocked)")
)

// NewHopper opens the current slot's socket, so Send works at once, and
// starts hopping in the background. Call Close to stop.
func NewHopper(cfg Config) (*Hopper, error) {
	if cfg.Schedule == nil {
		return nil, errors.New("direct: Session and Schedule are required")
	}
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	h := &Hopper{
		cfg:   cfg,
		ports: cfg.Ports,
		socks: map[uint16]*hopSocket{},
		slots: map[uint64]*slotState{},
		done:  make(chan struct{}),
		first: cfg.Schedule.SlotAt(cfg.Schedule.Now()),
	}
	if h.ports == nil {
		h.ports, h.ownPorts = NewPorts(cfg.LocalIP), true
	}
	h.side, h.peerSide = sides(cfg.Session.Initiator())
	if err := h.ports.add(h); err != nil {
		return nil, err
	}
	h.step()
	h.wg.Add(1)
	go h.loop()
	return h, nil
}

// Send encrypts payload and sends it to the peer's port for the current
// slot. Safe for concurrent use. Errors count as loss.
func (h *Hopper) Send(payload []byte) error {
	if h.blocked() {
		h.stats.sendErrors.Add(1)
		return ErrBlocked
	}
	st := h.currentSlot()
	if st == nil || st.sock == nil {
		h.stats.sendErrors.Add(1)
		return ErrNoSocket
	}
	pkt, err := h.cfg.Session.Seal(make([]byte, 0, len(payload)+noise.Overhead), TypeData, payload)
	if err == nil {
		// Can race with the socket's close; that counts as a lost packet.
		_, err = st.sock.conn.WriteToUDPAddrPort(pkt, netip.AddrPortFrom(h.cfg.PeerIP, st.peerPort))
	}
	if err != nil {
		h.stats.sendErrors.Add(1)
		return err
	}
	h.stats.sent.Add(1)
	return nil
}

// currentSlot returns the current slot, opening it if housekeeping fell
// behind (it normally opens slots Lead early) rather than dropping packets.
func (h *Hopper) currentSlot() *slotState {
	slot := h.cfg.Schedule.SlotAt(h.cfg.Schedule.Now())
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.slots[slot]
	if st == nil && !h.closed() {
		st = h.openSlot(slot)
		h.stats.lateOpens.Add(1)
	}
	return st
}

// Current returns the current slot and both ports in it.
func (h *Hopper) Current() (slot uint64, local, peer uint16) {
	s := h.cfg.Schedule
	slot = s.SlotAt(s.Now())
	return slot, s.Port(h.side, slot), s.Port(h.peerSide, slot)
}

// Close stops hopping and gives back every socket. When it returns, no
// OnPacket call is running or will start. Slots still open are not counted
// as hops: their window never ended.
func (h *Hopper) Close() error {
	h.once.Do(func() {
		close(h.done)
		h.ports.remove(h)
		h.live.Lock() // waits out packets being handled; later ones see done
		h.live.Unlock()
		h.mu.Lock()
		for port := range h.socks {
			h.ports.release(port)
			delete(h.socks, port)
		}
		h.mu.Unlock()
		h.wg.Wait()
		if h.ownPorts {
			h.ports.Close()
		}
	})
	return nil
}

func (h *Hopper) blocked() bool { return h.cfg.Blocked != nil && h.cfg.Blocked() }

func (h *Hopper) closed() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// openSlot binds our port for slot. Called with h.mu held.
func (h *Hopper) openSlot(slot uint64) *slotState {
	s := h.cfg.Schedule
	st := &slotState{peerPort: s.Port(h.peerSide, slot)}
	st.sock = h.socket(s.Port(h.side, slot))
	h.slots[slot] = st
	return st
}

// socket returns our socket for port, taking it from the pool if needed.
// nil means the port is taken by another program and the slot is lost for
// inbound traffic. Called with h.mu held.
func (h *Hopper) socket(port uint16) *hopSocket {
	if sk, ok := h.socks[port]; ok {
		return sk
	}
	conn, err := h.ports.acquire(port)
	if err != nil {
		h.stats.bindFailed.Add(1)
		h.logf("hop: cannot bind port %d: %v", port, err)
		return nil
	}
	sk := &hopSocket{conn: conn, port: port}
	h.socks[port] = sk
	return sk
}

func (h *Hopper) logf(format string, args ...any) {
	if h.cfg.Logf != nil {
		h.cfg.Logf(format, args...)
	}
}

func (h *Hopper) String() string {
	slot, local, peer := h.Current()
	return fmt.Sprintf("hopper[%c slot=%d local=%d peer=%v:%d]", h.side, slot, local, h.cfg.PeerIP, peer)
}
