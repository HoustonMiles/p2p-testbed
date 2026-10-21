package direct

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"p2ptestbed/p2p/internal/noise"
	"time"
)

// receive handles one datagram that the Ports routed here by its receiver
// index. The checks run cheapest first, which is the DoS defence: receiver
// index (in Ports), source IP, then the replay window, then decryption.
//
// TODO(paper §5 Measures #5, countermeasures "per-packet authentication
// tags"): junk from the peer's own IP with a valid index still costs a full
// AEAD check. Add a cheap per-slot tag that is checked before decrypting.
func (h *Hopper) receive(port uint16, from netip.AddrPort, pkt, plain []byte) {
	h.live.RLock()
	defer h.live.RUnlock()
	h.mu.Lock()
	sk := h.socks[port]
	h.mu.Unlock()
	if sk == nil || h.closed() {
		return // given back meanwhile
	}
	typ, payload, ok := h.open(from, pkt, plain)
	if !ok {
		return
	}
	h.markArrival(sk, typ)
	h.dispatch(sk, typ, Inbound{Payload: payload, From: from, Port: port, At: time.Now()})
}

// open filters and decrypts one datagram, counting what it drops.
func (h *Hopper) open(from netip.AddrPort, pkt, dst []byte) (byte, []byte, bool) {
	if from.Addr() != h.cfg.PeerIP {
		h.stats.foreign.Add(1)
		return 0, nil, false
	}
	typ, payload, err := h.cfg.Session.Open(dst, pkt)
	switch {
	case errors.Is(err, noise.ErrReplay):
		h.stats.replayed.Add(1)
	case err != nil:
		h.stats.authFailed.Add(1)
	}
	return typ, payload, err == nil
}

func (h *Hopper) markArrival(sk *hopSocket, typ byte) {
	sk.heard.Store(true)
	t := h.cfg.Schedule.Now().UnixNano()
	sk.lastAny.Store(t)
	if typ == TypeData {
		sk.lastData.Store(t)
	}
}

func (h *Hopper) dispatch(sk *hopSocket, typ byte, in Inbound) {
	switch typ {
	case TypeProbe:
		h.stats.probesRecv.Add(1)
		h.notePeerHeard(sk, in.Payload)
	case TypeData:
		h.stats.received.Add(1)
		if h.cfg.OnPacket != nil {
			h.cfg.OnPacket(in)
		}
	}
}

// notePeerHeard records that the peer hears us on the probe's slot, so our
// direction works too and step can slow that slot's probing.
func (h *Hopper) notePeerHeard(sk *hopSocket, body []byte) {
	if len(body) < 9 || body[8]&probeHeard == 0 {
		return
	}
	slot := binary.BigEndian.Uint64(body)
	h.mu.Lock()
	if st := h.slots[slot]; st != nil && st.sock == sk {
		st.peerHeard = true
	}
	h.mu.Unlock()
}
