package direct

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// Hopper moves the direct channel from port to port on the shared Schedule.
//
// Around each slot boundary, for slot s:
//
//		   s-1 socket open                 s socket open
//		|----------------------|.....|..........|------------------|
//		                    Lead  ^   drain
//		                  boundary of slot s
//
//	 1. Lead before s starts: bind our port for s and send probes from it to
//	    the peer's port for s. The peer does the same towards us at the same
//	    time, so each side's probes open its own NAT's pinhole for the other's
//	    (the same simultaneous-open trick as the first hole punch, repeated
//	    on every hop).
//	 2. At the boundary: Send switches to the slot-s socket and the peer's
//	    slot-s port.
//	 3. Drain: keep the slot s-1 socket open until the packets the peer sent
//	    to it have arrived, then close it. See drained() for the rule: in
//	    short, once traffic has moved to the new port and the old one has
//	    been quiet for DrainIdle, with MinOverlap as the floor and
//	    MaxOverlap as the ceiling.
//
// Why not a fixed overlap: packets for the old port can still be sitting in
// a router queue. A full 1000-packet buffer at 50 Mbit/s holds ~200 ms, and
// a fixed 100 ms overlap lost ~8% of a saturated stream at every hop. The
// drain rule waits exactly as long as the queue takes, and no longer.
//
// Lead and MinOverlap are the clock tolerance: hops keep working while the
// two clocks differ by less than min(Lead, MinOverlap), even on an idle
// link.
//
// Requires both NATs to keep the local port as the public port (see
// Schedule). Retired ports need no handling here: once the socket is closed
// the kernel drops what arrives for it.
type Hopper struct {
	cfg      Config
	peerRole byte

	mu    sync.Mutex
	socks map[uint16]*hopSocket // open sockets by local port
	slots map[uint64]*slotState // slots in their open window
	last  uint64                // last slot logged as current
	first uint64                // the slot in progress when the Hopper started

	// How long old sockets stayed open after their slot ended (under mu).
	drainCount   uint64
	drainTotal   time.Duration
	drainCutoffs uint64

	stats counters
	done  chan struct{}
	wg    sync.WaitGroup
	once  sync.Once
}

// Config for a Hopper. Zero durations take the defaults noted.
type Config struct {
	Session  *Session  // from a finished Handshake
	Schedule *Schedule // built from Session.HopKey(); same on both peers
	Role     byte      // 'a' or 'b'; the peer is the other one
	PeerIP   netip.Addr
	LocalIP  netip.Addr // address to bind; zero = all interfaces

	Lead time.Duration // default Interval/10, at least 2*Tick

	// When to close the previous slot's socket after a boundary (see
	// drained). MinOverlap: never sooner, default Interval/10 (this is the
	// clock tolerance on an idle link). MaxOverlap: always by then, default
	// Interval/2; Lead + MaxOverlap must stay under Interval. DrainIdle: how
	// long the old port must be quiet once traffic has moved, default 50 ms
	// (covers reordering by jitter, well under the gaps of any real stream).
	MinOverlap time.Duration
	MaxOverlap time.Duration
	DrainIdle  time.Duration

	ProbeInterval time.Duration // until the peer is heard on a slot; default 20 ms
	KeepAlive     time.Duration // after that, keeps the pinhole open; default 10 s (UDP_TIMEOUT is 30)
	Tick          time.Duration // housekeeping resolution; default 5 ms

	// OnPacket is called for every authenticated Data packet, on the reader
	// goroutine of the socket it arrived on: it must be safe for concurrent
	// use and should be fast (count, or copy and queue). Payload is only
	// valid during the call.
	OnPacket func(Inbound)

	Logf func(format string, args ...any)
}

// Inbound is one received Data packet.
type Inbound struct {
	Payload []byte
	From    netip.AddrPort
	Port    uint16    // our local port it arrived on
	At      time.Time // real (not skewed) receive time
}

// Stats are counts since the Hopper started. The JSON names carry a hop_
// prefix so the struct can be embedded in the punch tool's flat Result.
type Stats struct {
	Sent       uint64 `json:"hop_sent"`        // Data packets sent
	SendErrors uint64 `json:"hop_send_errors"` // no socket for the slot, or write failed
	Received   uint64 `json:"hop_received"`    // authenticated Data packets
	ProbesSent uint64 `json:"hop_probes_sent"`
	ProbesRecv uint64 `json:"hop_probes_recv"`

	// Dropped on arrival. Foreign: not from the peer's IP, dropped before
	// any crypto. AuthFailed: from the peer's IP but forged or corrupted.
	// Replayed: a valid packet seen before.
	Foreign    uint64 `json:"hop_foreign"`
	AuthFailed uint64 `json:"hop_auth_failed"`
	Replayed   uint64 `json:"hop_replayed"`

	// Hops, counted when a slot's window closes. OK: something from the
	// peer arrived on our port for that slot, i.e. the inbound path worked
	// on that hop (the peer reports the other direction). Missed: nothing
	// arrived. BindFailed: our port for the slot was taken.
	//
	// The slot in progress when the Hopper started is not counted: the two
	// peers start at slightly different moments, so it may be over for the
	// peer before it even began here.
	HopsOK     uint64 `json:"hop_hops_ok"`
	HopsMissed uint64 `json:"hop_hops_missed"`
	BindFailed uint64 `json:"hop_bind_failed"`

	// LateOpens: Send found the current slot not open yet because
	// housekeeping fell behind (CPU starvation), and opened it itself.
	// Should be 0; if not, the peer may have missed packets at that hop
	// too, since its socket opens on the same kind of timer.
	LateOpens uint64 `json:"hop_late_opens"`

	// DrainMsAvg: how long the previous slot's port stayed open after the
	// boundary, on average, in ms. A mix of busy hops (closed once drained)
	// and idle hops (open until MaxOverlap), so read it with care.
	//
	// DrainCutoffs: old ports closed at MaxOverlap while Data from the peer
	// was still arriving on them (within the last DrainIdle). These are the
	// hops that can lose data: the queue towards us held more than
	// MaxOverlap's worth. Should be 0. Idle hops that reach MaxOverlap are
	// not counted, and neither are attack floods, which fail authentication
	// and so never count as arriving Data.
	DrainMsAvg   float64 `json:"hop_drain_ms_avg"`
	DrainCutoffs uint64  `json:"hop_drain_cutoffs"`
}

// counters mirrors Stats with atomics, since readers update it concurrently.
type counters struct {
	sent, sendErrors, received, probesSent, probesRecv atomic.Uint64
	foreign, authFailed, replayed                      atomic.Uint64
	hopsOK, hopsMissed, bindFailed, lateOpens          atomic.Uint64
}

type hopSocket struct {
	conn  *net.UDPConn
	port  uint16
	heard atomic.Bool // an authenticated packet from the peer arrived here

	// Arrival times of the latest authenticated packet of any type, and of
	// the latest Data packet, as UnixNano on the schedule's clock (so they
	// compare with slot boundaries even when the clock is skewed).
	lastAny, lastData atomic.Int64
}

type slotState struct {
	sock      *hopSocket // nil if the bind failed
	peerPort  uint16     // the peer's port in this slot
	lastProbe time.Time
	peerHeard bool // the peer's probes say it has heard ours on this slot
}

var ErrNoSocket = errors.New("direct: no socket for the current slot")

// NewHopper opens the socket for the current slot (so Send works at once)
// and starts hopping in the background. Call Close to stop.
func NewHopper(cfg Config) (*Hopper, error) {
	if cfg.Session == nil || cfg.Schedule == nil {
		return nil, errors.New("direct: Session and Schedule are required")
	}
	if cfg.Role != 'a' && cfg.Role != 'b' {
		return nil, errors.New("direct: role must be a or b")
	}
	if !cfg.PeerIP.IsValid() {
		return nil, errors.New("direct: PeerIP is required")
	}
	if cfg.Tick == 0 {
		cfg.Tick = 5 * time.Millisecond
	}
	if cfg.Lead == 0 {
		cfg.Lead = max(cfg.Schedule.Interval/10, 2*cfg.Tick)
	}
	if cfg.MinOverlap == 0 {
		cfg.MinOverlap = cfg.Schedule.Interval / 10
	}
	if cfg.MaxOverlap == 0 {
		cfg.MaxOverlap = cfg.Schedule.Interval / 2
	}
	if cfg.DrainIdle == 0 {
		cfg.DrainIdle = 50 * time.Millisecond
	}
	if cfg.ProbeInterval == 0 {
		cfg.ProbeInterval = 20 * time.Millisecond
	}
	if cfg.KeepAlive == 0 {
		cfg.KeepAlive = 10 * time.Second
	}
	// The next socket must be open before the boundary, and housekeeping
	// only runs every Tick, so Lead has to cover at least one Tick.
	if cfg.Lead < cfg.Tick {
		return nil, errors.New("direct: Lead must be at least Tick")
	}
	if cfg.MinOverlap > cfg.MaxOverlap {
		return nil, errors.New("direct: MinOverlap must not exceed MaxOverlap")
	}
	if cfg.Lead+cfg.MaxOverlap >= cfg.Schedule.Interval {
		return nil, errors.New("direct: Lead + MaxOverlap must be shorter than the slot Interval")
	}
	cfg.PeerIP = cfg.PeerIP.Unmap()

	h := &Hopper{
		cfg:      cfg,
		peerRole: 'a' + 'b' - cfg.Role,
		socks:    map[uint16]*hopSocket{},
		slots:    map[uint64]*slotState{},
		done:     make(chan struct{}),
	}
	h.first = cfg.Schedule.SlotAt(cfg.Schedule.Now())
	h.step()
	h.wg.Add(1)
	go h.loop()
	return h, nil
}

// Send encrypts payload and sends it from this slot's port to the peer's
// port for this slot. Safe for concurrent use.
func (h *Hopper) Send(payload []byte) error {
	slot := h.cfg.Schedule.SlotAt(h.cfg.Schedule.Now())
	h.mu.Lock()
	st := h.slots[slot]
	if st == nil && !h.closed() {
		// Housekeeping is late (it normally opens the slot Lead early):
		// open it here rather than drop packets until it catches up.
		st = h.openSlot(slot)
		h.stats.lateOpens.Add(1)
	}
	h.mu.Unlock()
	if st == nil || st.sock == nil {
		h.stats.sendErrors.Add(1)
		return ErrNoSocket
	}
	pkt, err := h.cfg.Session.Seal(make([]byte, 0, len(payload)+Overhead), TypeData, payload)
	if err != nil {
		h.stats.sendErrors.Add(1)
		return err
	}
	// A write racing with the socket's close fails here and counts as an
	// error, the same as a packet lost in the network.
	if _, err := st.sock.conn.WriteToUDPAddrPort(pkt, netip.AddrPortFrom(h.cfg.PeerIP, st.peerPort)); err != nil {
		h.stats.sendErrors.Add(1)
		return err
	}
	h.stats.sent.Add(1)
	return nil
}

// Current returns the current slot and both ports in it.
func (h *Hopper) Current() (slot uint64, local, peer uint16) {
	s := h.cfg.Schedule
	slot = s.SlotAt(s.Now())
	return slot, s.Port(h.cfg.Role, slot), s.Port(h.peerRole, slot)
}

// Stats returns a snapshot of the counters.
func (h *Hopper) Stats() Stats {
	c := &h.stats
	st := Stats{
		Sent: c.sent.Load(), SendErrors: c.sendErrors.Load(), Received: c.received.Load(),
		ProbesSent: c.probesSent.Load(), ProbesRecv: c.probesRecv.Load(),
		Foreign: c.foreign.Load(), AuthFailed: c.authFailed.Load(), Replayed: c.replayed.Load(),
		HopsOK: c.hopsOK.Load(), HopsMissed: c.hopsMissed.Load(), BindFailed: c.bindFailed.Load(),
		LateOpens: c.lateOpens.Load(),
	}
	h.mu.Lock()
	if h.drainCount > 0 {
		st.DrainMsAvg = ms(h.drainTotal) / float64(h.drainCount)
	}
	st.DrainCutoffs = h.drainCutoffs
	h.mu.Unlock()
	return st
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// Close stops hopping and closes every socket. Slots still open at that
// point are not counted in HopsOK/HopsMissed, since their window never ended.
func (h *Hopper) Close() error {
	h.once.Do(func() {
		close(h.done)
		h.mu.Lock()
		for port, sk := range h.socks {
			sk.conn.Close()
			delete(h.socks, port)
		}
		h.mu.Unlock()
		h.wg.Wait() // housekeeping and every reader have returned
	})
	return nil
}

// ---------------------------------------------------------------------------
// Housekeeping
// ---------------------------------------------------------------------------

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

// probe is a probe to send once the lock is released.
type probe struct {
	conn  *net.UDPConn
	to    netip.AddrPort
	slot  uint64
	heard bool // we have heard the peer on this slot
}

// Probe body: slot (u64, big-endian), then one flags byte.
const probeHeard = 1 // flag: the sender has heard the receiver on this slot

// step brings the open sockets in line with the schedule: which slots are
// in their window right now, which ports they need, and which still need
// probing. Times all come from the schedule's clock, so a skewed clock skews
// the whole hopper consistently.
func (h *Hopper) step() {
	s := h.cfg.Schedule
	now := s.Now()
	cur := s.SlotAt(now)
	pos := now.Sub(s.SlotStart(cur))

	want := map[uint64]bool{cur: true}
	if s.Interval-pos <= h.cfg.Lead {
		want[cur+1] = true // next slot, opening early
	}

	h.mu.Lock()
	if h.closed() { // Close raced with this step; don't open anything new
		h.mu.Unlock()
		return
	}

	// The previous slot stays while it drains. Only one that is already
	// open: a slot from before we started is never reopened.
	if old, ok := h.slots[cur-1]; ok && cur > 0 {
		var newSock *hopSocket
		if st := h.slots[cur]; st != nil {
			newSock = st.sock
		}
		closeOld, cutoff := drained(h.cfg, pos, now.UnixNano(), s.SlotStart(cur).UnixNano(), old.sock, newSock)
		if !closeOld {
			want[cur-1] = true
		} else if cur-1 > h.first {
			h.drainCount++
			h.drainTotal += pos
			if cutoff {
				h.drainCutoffs++
				h.logf("hop: slot %d: old port cut off at %v while data was still arriving", cur-1, pos)
			}
		}
	}

	// 1. Retire slots whose window has passed and record whether the hop
	//    worked.
	for slot, st := range h.slots {
		if want[slot] {
			continue
		}
		delete(h.slots, slot)
		if slot <= h.first {
			continue // started before we did; see Stats
		}
		if st.sock != nil && st.sock.heard.Load() {
			h.stats.hopsOK.Add(1)
		} else {
			h.stats.hopsMissed.Add(1)
			h.logf("hop: slot %d: nothing arrived from the peer", slot)
		}
	}

	// 2. Open slots entering their window.
	for slot := range want {
		if _, ok := h.slots[slot]; !ok {
			h.openSlot(slot)
		}
	}

	// 3. Close sockets no slot uses any more. (Two slots in a row can draw
	//    the same port; they then share one socket.)
	used := map[uint16]bool{}
	for _, st := range h.slots {
		if st.sock != nil {
			used[st.sock.port] = true
		}
	}
	for port, sk := range h.socks {
		if !used[port] {
			sk.conn.Close() // its reader goroutine sees the error and exits
			delete(h.socks, port)
		}
	}

	// 4. Probes: often until the path works both ways on the slot, then only
	//    as a keepalive so the NAT pinholes outlive long slots. "Both ways"
	//    needs the peer's word: hearing the peer only proves its direction,
	//    so each probe says whether its sender has heard us. Stopping as soon
	//    as WE hear would leave the peer deaf on that slot until data flows.
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

	if cur != h.last {
		h.last = cur
		if st := h.slots[cur]; st != nil && st.sock != nil {
			h.logf("hop: slot %d, local port %d, peer port %d", cur, st.sock.port, st.peerPort)
		}
	}
	h.mu.Unlock()

	// Send outside the lock so a slow write never holds up Send or readers.
	for _, p := range probes {
		h.sendProbe(p)
	}
}

// drained says whether the previous slot's socket can close, pos into the
// new slot. now and boundary are UnixNano on the schedule's clock.
//
//   - Before MinOverlap: no. A peer whose clock is behind ours may still be
//     in the old slot; this is the clock tolerance.
//   - From MaxOverlap: yes, whatever is still arriving.
//   - In between: yes once (1) a Data packet has arrived on the new port
//     since the boundary, so the peer has moved on, and (2) nothing has
//     arrived on the old port for DrainIdle. Packets travel the same path
//     in order (apart from jitter), so when the newer packets on the new
//     port are arriving and the old port has gone quiet, the old port's
//     packets have all arrived. A full router queue just delays both, and
//     the old port stays open exactly that much longer.
//
// Without Data on the new port (an idle link, or a peer that only probes)
// the old socket stays until MaxOverlap.
//
// cutoff reports a close forced by MaxOverlap while Data was still
// arriving on the old port (within DrainIdle): the one case that loses
// packets, counted in Stats.DrainCutoffs.
func drained(cfg Config, pos time.Duration, now, boundary int64, old, cur *hopSocket) (closeOld, cutoff bool) {
	shared := old == nil || old == cur
	switch {
	case pos < cfg.MinOverlap:
		return false, false
	case pos >= cfg.MaxOverlap:
		busy := !shared && now-old.lastData.Load() < int64(cfg.DrainIdle)
		return true, busy
	case shared:
		// The bind failed (nothing to drain), or both slots drew the same
		// port and share the socket, which stays open for the new slot.
		return true, false
	case cur == nil:
		return false, false
	}
	moved := cur.lastData.Load() >= boundary
	quiet := now-old.lastAny.Load() >= int64(cfg.DrainIdle)
	return moved && quiet, false
}

// openSlot binds our port for slot and records the peer's. Called with
// h.mu held.
func (h *Hopper) openSlot(slot uint64) *slotState {
	s := h.cfg.Schedule
	st := &slotState{peerPort: s.Port(h.peerRole, slot)}
	st.sock = h.socket(s.Port(h.cfg.Role, slot))
	h.slots[slot] = st
	return st
}

// closed reports whether Close has been called.
func (h *Hopper) closed() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// socket returns the open socket for port, binding it if needed. nil means
// the bind failed (port in use); that slot is then lost for inbound traffic.
// Called with h.mu held.
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

func (h *Hopper) sendProbe(p probe) {
	body := binary.BigEndian.AppendUint64(make([]byte, 0, 9), p.slot)
	if p.heard {
		body = append(body, probeHeard)
	} else {
		body = append(body, 0)
	}
	pkt, err := h.cfg.Session.Seal(nil, TypeProbe, body)
	if err != nil {
		return
	}
	if _, err := p.conn.WriteToUDPAddrPort(pkt, p.to); err == nil {
		h.stats.probesSent.Add(1)
	}
}

// ---------------------------------------------------------------------------
// Receiving
// ---------------------------------------------------------------------------

// readLoop runs on its own goroutine for each open socket until the socket
// is closed. The order of the checks is the DoS defence: the cheapest test
// (source IP) first, then the replay window, then decryption.
func (h *Hopper) readLoop(sk *hopSocket) {
	defer h.wg.Done()
	buf := make([]byte, 65536)
	plain := make([]byte, 0, 65536)
	for {
		n, from, err := sk.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return // closed by step() or Close()
		}
		at := time.Now()
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		if from.Addr() != h.cfg.PeerIP {
			h.stats.foreign.Add(1)
			continue
		}
		typ, payload, err := h.cfg.Session.Open(plain[:0], buf[:n])
		switch {
		case errors.Is(err, ErrReplay):
			h.stats.replayed.Add(1)
			continue
		case err != nil:
			h.stats.authFailed.Add(1)
			continue
		}
		sk.heard.Store(true)
		t := h.cfg.Schedule.Now().UnixNano()
		sk.lastAny.Store(t)
		if typ == TypeData {
			sk.lastData.Store(t)
		}
		switch typ {
		case TypeProbe:
			h.stats.probesRecv.Add(1)
			// The peer has heard us on this slot: our direction works too,
			// so step() can slow this slot's probing to the keepalive.
			if len(payload) >= 9 && payload[8]&probeHeard != 0 {
				slot := binary.BigEndian.Uint64(payload)
				h.mu.Lock()
				if st := h.slots[slot]; st != nil && st.sock == sk {
					st.peerHeard = true
				}
				h.mu.Unlock()
			}
		case TypeData:
			h.stats.received.Add(1)
			if h.cfg.OnPacket != nil {
				h.cfg.OnPacket(Inbound{Payload: payload, From: from, Port: sk.port, At: at})
			}
		}
	}
}

func (h *Hopper) logf(format string, args ...any) {
	if h.cfg.Logf != nil {
		h.cfg.Logf("[%c] "+format, append([]any{h.cfg.Role}, args...)...)
	}
}

// String is for logs: the current slot and ports.
func (h *Hopper) String() string {
	slot, local, peer := h.Current()
	return fmt.Sprintf("hopper[%c slot=%d local=%d peer=%v:%d]", h.cfg.Role, slot, local, h.cfg.PeerIP, peer)
}

