package direct

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"time"
)

// readLoop runs for each open socket until it is closed. The checks run
// cheapest first, which is the DoS defence: source IP, then the replay
// window, then decryption.
//
// TODO(paper §5 Measures #5, countermeasures "per-packet authentication
// tags"): junk from the peer's own IP still costs a full AEAD check. Add a
// cheap per-slot tag that is checked before decrypting.
func (h *Hopper) readLoop(sk *hopSocket) {
	defer h.wg.Done()
	buf := make([]byte, 65536)
	plain := make([]byte, 0, 65536)
	for {
		n, from, err := sk.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return // closed by step or Close
		}
		in := Inbound{From: netip.AddrPortFrom(from.Addr().Unmap(), from.Port()), Port: sk.port, At: time.Now()}
		typ, payload, ok := h.open(in.From, buf[:n], plain[:0])
		if !ok {
			continue
		}
		in.Payload = payload
		h.markArrival(sk, typ)
		h.dispatch(sk, typ, in)
	}
}

// open filters and decrypts one datagram, counting what it drops.
func (h *Hopper) open(from netip.AddrPort, pkt, dst []byte) (byte, []byte, bool) {
	if from.Addr() != h.cfg.PeerIP {
		h.stats.foreign.Add(1)
		return 0, nil, false
	}
	typ, payload, err := h.cfg.Session.Open(dst, pkt)
	switch {
	case errors.Is(err, ErrReplay):
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

