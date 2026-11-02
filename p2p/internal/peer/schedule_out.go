package peer

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// writeScheduleOut writes the ports this peer will receive data on over the
// coming run to Config.HopScheduleOut, one "<unix_ms> <port>" line per slot.
// It is a testbed hook for the reactive attacker (an oracle that is told the
// active port but reacts with a delay); a real attacker cannot see this.
// Under a NAT that keeps the local port, the local port is the public port.
//
// In direct mode the lines are the hopper's slot schedule; in raw mode the
// one fixed bound port, valid for the whole run.
func (r *runner) writeScheduleOut(lines []string) {
	if r.cfg.HopScheduleOut == "" {
		return
	}
	data := strings.Join(lines, "\n") + "\n"
	// Write-then-rename so the attacker (a reader on the shared volume) sees
	// either the whole schedule or none of it, never a half-written file.
	tmp := r.cfg.HopScheduleOut + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0o644); err != nil {
		r.logf("schedule-out: %v", err)
		return
	}
	if err := os.Rename(tmp, r.cfg.HopScheduleOut); err != nil {
		r.logf("schedule-out: %v", err)
		return
	}
	r.logf("schedule-out: wrote %d slots to %s", len(lines), r.cfg.HopScheduleOut)
}

// hopScheduleLines is the hopping schedule for the next horizon.
func (l *link) hopScheduleLines(horizon time.Duration) []string {
	var out []string
	for _, sp := range l.hopper.Upcoming(horizon) {
		out = append(out, fmt.Sprintf("%d %d", sp.UnixMs, sp.Port))
	}
	return out
}

// rawScheduleLine is the single fixed receive port in raw mode.
func (r *runner) rawScheduleLine() []string {
	port := r.conn.LocalAddr().(*net.UDPAddr).AddrPort().Port()
	return []string{fmt.Sprintf("%d %d", time.Now().UnixMilli(), port)}
}

func (r *runner) scheduleHorizon() time.Duration {
	c := r.cfg
	return 3*(c.Duration+c.Drain) + c.Linger + 30*time.Second
}
