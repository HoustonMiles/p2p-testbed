package peer

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// readSchedule parses a schedule-out file into (unix ms, port) pairs.
func readSchedule(t *testing.T, path string) [][2]int64 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("schedule file: %v", err)
	}
	var out [][2]int64
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			t.Fatalf("bad schedule line %q", line)
		}
		ms, _ := strconv.ParseInt(f[0], 10, 64)
		port, _ := strconv.ParseInt(f[1], 10, 64)
		out = append(out, [2]int64{ms, port})
	}
	return out
}

// In direct mode the schedule-out lists the hop ports over time: several
// slots, ports in the hopper's range, start times that increase.
func TestScheduleOutDirect(t *testing.T) {
	dir := t.TempDir()
	out := func(c *Config) { c.HopScheduleOut = filepath.Join(dir, c.Name+".txt") }
	results := runPair(t, directConfig("kk", "file"), withKeys, out)
	checkResults(t, results)

	sched := readSchedule(t, filepath.Join(dir, "a.txt"))
	if len(sched) < 3 {
		t.Fatalf("only %d slots in the schedule, want several", len(sched))
	}
	for i, sp := range sched {
		if sp[1] < 10000 || sp[1] > 32767 {
			t.Errorf("slot %d port %d outside the hop range", i, sp[1])
		}
		if i > 0 && sp[0] <= sched[i-1][0] {
			t.Errorf("slot %d start %d not after %d", i, sp[0], sched[i-1][0])
		}
	}
}

// In raw mode the schedule-out is the one fixed receive port.
func TestScheduleOutRaw(t *testing.T) {
	dir := t.TempDir()
	out := func(c *Config) { c.HopScheduleOut = filepath.Join(dir, c.Name+".txt") }
	results := runPair(t, Config{}, out)
	checkResults(t, results)

	sched := readSchedule(t, filepath.Join(dir, "a.txt"))
	if len(sched) != 1 {
		t.Fatalf("raw mode wrote %d lines, want 1", len(sched))
	}
	if sched[0][1] < 1 || sched[0][1] > 65535 {
		t.Errorf("raw port %d is not a port", sched[0][1])
	}
}
