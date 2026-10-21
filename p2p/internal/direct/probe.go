package direct

import (
	"encoding/binary"
	"net"
	"net/netip"
	"time"
)

// probe is a probe to send once the lock is released.
type probe struct {
	conn  *net.UDPConn
	to    netip.AddrPort
	slot  uint64
	heard bool
}

const probeHeard = 1 // flags bit: the sender has heard the receiver on this slot

// dueProbes picks the slots to probe now: often until the path works both
// ways, then only as a keepalive. Hearing the peer proves only its
// direction, so the peer's probes carry whether it hears us; stopping as
// soon as we hear would leave the peer deaf on that slot until data flows.
func (h *Hopper) dueProbes(now time.Time) []probe {
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
	return probes
}

func (h *Hopper) sendProbe(p probe) {
	var flags byte
	if p.heard {
		flags = probeHeard
	}
	body := append(binary.BigEndian.AppendUint64(make([]byte, 0, 9), p.slot), flags)
	pkt, err := h.cfg.Session.Seal(nil, TypeProbe, body)
	if err != nil {
		return
	}
	if _, err := p.conn.WriteToUDPAddrPort(pkt, p.to); err == nil {
		h.stats.probesSent.Add(1)
	}
}
