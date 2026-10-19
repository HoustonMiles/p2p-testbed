package peer

import (
	"net/netip"
	"time"

	"p2ptestbed/p2p/internal/wire"
)

// punchAll sends Punch to every member until each answers with a PunchAck.
// Behind an endpoint-dependent NAT a member answers from another address
// than the rendezvous saw; learn switches to it. A member that never
// answers is a result (punched = false), not an error.
func (r *runner) punchAll() {
	ls, start := r.active(), time.Since(r.t0)
	r.pump(r.cfg.PunchTimeout, r.cfg.PunchInterval,
		func() {
			for _, l := range ls {
				if !l.gotAck {
					l.res.PunchesSent++
					r.sendTo(l.remote, wire.Punch, uint64(l.res.PunchesSent), nil)
				}
			}
		},
		func() bool { return allLinks(ls, func(l *link) bool { return l.gotAck }) })
	for _, l := range ls {
		l.finishPunch(start)
	}
}

func (l *link) finishPunch(start time.Duration) {
	l.res.PeerAddr = ptr(l.remote.String())
	l.res.PeerAddrChanged = ptr(l.remote != l.rdvAddr)
	if !l.gotAck {
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

func allLinks(ls []*link, ok func(*link) bool) bool {
	for _, l := range ls {
		if !ok(l) {
			return false
		}
	}
	return true
}
