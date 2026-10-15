package direct

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
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
// TODO(paper §5 Dynamic Handoff Mode): report missed hops and drain cutoffs
// as they happen (a callback, not only Stats) so the Handoff Manager can
// pivot to the relay while the session runs.
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

	mu    sync.Mutex
	socks map[uint16]*hopSocket // by local port; consecutive slots may share one
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

var ErrNoSocket = errors.New("direct: no socket for the current slot")

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
		socks: map[uint16]*hopSocket{},
		slots: map[uint64]*slotState{},
		done:  make(chan struct{}),
		first: cfg.Schedule.SlotAt(cfg.Schedule.Now()),
	}
	h.side, h.peerSide = cfg.Session.sides()
	h.step()
	h.wg.Add(1)
	go h.loop()
	return h, nil
}

// Send encrypts payload and sends it to the peer's port for the current
// slot. Safe for concurrent use. Errors count as loss.
func (h *Hopper) Send(payload []byte) error {
	st := h.currentSlot()
	if st == nil || st.sock == nil {
		h.stats.sendErrors.Add(1)
		return ErrNoSocket
	}
	pkt, err := h.cfg.Session.Seal(make([]byte, 0, len(payload)+Overhead), TypeData, payload)
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

// Close stops hopping and closes every socket. Slots still open are not
// counted as hops: their window never ended.
func (h *Hopper) Close() error {
	h.once.Do(func() {
		close(h.done)
		h.mu.Lock()
		for port, sk := range h.socks {
			sk.conn.Close()
			delete(h.socks, port)
		}
		h.mu.Unlock()
		h.wg.Wait()
	})
	return nil
}

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

// socket returns the open socket for port, binding it if needed. nil means
// the port is taken and the slot is lost for inbound traffic. Called with
// h.mu held.
func (h *Hopper) socket(port uint16) *hopSocket {
	if sk, ok := h.socks[port]; ok {
		return sk
	}
	var ip net.IP
	if h.cfg.LocalIP.IsValid() {
		ip = h.cfg.LocalIP.AsSlice()
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: int(port)})
	if err != nil {
		h.stats.bindFailed.Add(1)
		h.logf("hop: cannot bind port %d: %v", port, err)
		return nil
	}
	conn.SetReadBuffer(4 << 20)
	conn.SetWriteBuffer(4 << 20)
	sk := &hopSocket{conn: conn, port: port}
	h.socks[port] = sk
	h.wg.Add(1)
	go h.readLoop(sk)
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
