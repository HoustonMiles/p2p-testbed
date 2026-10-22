package peer

import (
	"net/netip"
	"time"

	"p2ptestbed/p2p/internal/wire"
)

// punch sends Punch to the member until it answers with a PunchAck.
// Behind an endpoint-dependent NAT a member answers from another address
// than the rendezvous saw; learn switches to it. A member that never
// answers is a result (punched = false), not an error.
func (l *link) punch() bool {
	start := time.Since(l.r.t0)
	l.pump(l.r.cfg.PunchTimeout, l.r.cfg.PunchInterval,
		func() {
			if !l.gotAck {
				l.res.PunchesSent++
				l.r.sendTo(l.remote, wire.Punch, uint64(l.res.PunchesSent), nil)
			}
		},
		func() bool { return l.gotAck })
	l.finishPunch(start)
	return l.gotAck
}

func (l *link) finishPunch(start time.Duration) {
	l.res.PeerAddr = ptr(l.remote.String())
	l.res.PeerAddrChanged = ptr(l.remote != l.rdvAddr)
	switch {
	case !l.gotAck && l.r.cfg.Handoff:
		l.logf("punch: no reply from %v; the relay carries the link", l.remote)
		return
	case !l.gotAck:
		l.stop("punch: no reply from " + l.remote.String())
		return
	}
	l.res.Punched = true
	l.res.PunchMs = ptr(ms(l.ackAt - start))
	l.logf("punch: connected to %v in %.1f ms", l.remote, *l.res.PunchMs)
}

func (l *link) learn(from netip.AddrPort) {
	if from == l.remote {
		return
	}
	l.logf("punch: the member answers from %v, switching from %v", from, l.remote)
	l.remote = from
}

func (l *link) onPunch(m inbound) {
	l.res.PunchesRecv++
	l.learn(m.from)
	l.r.sendTo(m.from, wire.PunchAck, m.pkt.Seq, nil)
}

func (l *link) onPunchAck(m inbound) {
	l.learn(m.from)
	if !l.gotAck {
		l.gotAck, l.ackAt = true, m.at
	}
}
