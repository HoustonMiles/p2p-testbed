// Command flood is the static attacker for Experiment C: it sends UDP
// packets at a fixed rate to one fixed address and port for a fixed
// window, and never adapts. It is a traffic generator for the testbed's
// isolated network, nothing more: it follows no hops, reads no replies and
// learns nothing about its target. The scripted reactive attacker,
// which does adapt, is a separate tool.
//
// Against a peer that hops ports, a fixed-port flood lands only while that
// port is the active one, so its effect on goodput is what Experiment C
// measures. Against a fixed address it can still fill the access link
// whatever the port, which is the volumetric case only the relay escapes.
//
// It prints one JSON line when it finishes, so the runner can mark the
// attack window on the peers' goodput-over-time series:
//
//	{"target":"198.18.1.10:40000","start_unix_ms":...,"end_unix_ms":...,
//	 "packets":...,"bytes":...,"mbit":...}
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net"
	"os"
	"sort"
	"time"
)

func main() {
	target := flag.String("target", "", "static: address and port to flood, e.g. 198.18.1.10:40000")
	mbit := flag.Float64("mbit", 0, "offered load in Mbit/s (required)")
	size := flag.Int("size", 1200, "UDP payload bytes per packet")
	at := flag.Duration("at", 0, "wait this long before starting")
	duration := flag.Duration("duration", 0, "flood for this long (required)")
	quiet := flag.Bool("quiet", false, "no progress logging on stderr")
	// Reactive mode (the scripted attacker, an oracle with a reaction delay):
	schedule := flag.String("schedule", "", "reactive: file of '<unix_ms> <port>' the victim will receive on")
	targetIP := flag.String("target-ip", "", "reactive: the victim's address (port comes from -schedule)")
	reactDelay := flag.Duration("react-delay", 0, "reactive: flood the port the victim used this long ago")
	flag.Parse()

	if *mbit <= 0 || *duration <= 0 {
		fmt.Fprintln(os.Stderr, "flood needs -mbit and -duration")
		os.Exit(2)
	}
	var res result
	var err error
	if *schedule != "" {
		if *targetIP == "" {
			fmt.Fprintln(os.Stderr, "reactive flood needs -target-ip")
			os.Exit(2)
		}
		res, err = runReactive(*targetIP, *schedule, *reactDelay, *mbit, *size, *at, *duration)
	} else {
		if *target == "" {
			fmt.Fprintln(os.Stderr, "flood needs -target (or -schedule for reactive)")
			os.Exit(2)
		}
		res, err = run(*target, *mbit, *size, *at, *duration)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "flood:", err)
		os.Exit(1)
	}
	if !*quiet {
		fmt.Fprintf(os.Stderr, "flood: sent %d packets (%.1f Mbit/s) to %s\n",
			res.Packets, res.sentMbit(), res.Target)
	}
	line, _ := json.Marshal(res)
	fmt.Println(string(line))
}

// result is the one JSON line the run prints: the attack window on the
// peers' clock (start_unix_ms puts it on the same timeline as their
// goodput series) and what was sent.
type result struct {
	Target      string  `json:"target"`
	StartUnixMs float64 `json:"start_unix_ms"`
	EndUnixMs   float64 `json:"end_unix_ms"`
	Packets     int64   `json:"packets"`
	Bytes       int64   `json:"bytes"`
	Mbit        float64 `json:"mbit"` // the offered rate, not necessarily the sent rate
}

func (r result) sentMbit() float64 {
	if s := (r.EndUnixMs - r.StartUnixMs) / 1000; s > 0 {
		return float64(r.Bytes) * 8 / s / 1e6
	}
	return 0
}

func run(target string, mbit float64, size int, at, duration time.Duration) (result, error) {
	addr, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		return result{}, err
	}
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return result{}, err
	}
	defer conn.Close()
	conn.SetWriteBuffer(4 << 20)

	if at > 0 {
		time.Sleep(at)
	}
	pps := mbit * 1e6 / float64(size*8)
	pkt := make([]byte, size)
	copy(pkt, "FLOOD") // a marker, so a capture shows what these packets are

	var packets, bytes int64
	start := time.Now()
	deadline := start.Add(duration)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for now := start; now.Before(deadline); now = <-tick.C {
		due := int64(math.Min(now.Sub(start).Seconds(), duration.Seconds()) * pps)
		for packets < due {
			if _, err := conn.Write(pkt); err == nil {
				bytes += int64(size)
			}
			packets++
		}
	}
	end := time.Now()
	return result{
		Target:      target,
		StartUnixMs: float64(start.UnixMicro()) / 1000,
		EndUnixMs:   float64(end.UnixMicro()) / 1000,
		Packets:     packets,
		Bytes:       bytes,
		Mbit:        mbit,
	}, nil
}

// --- reactive mode -----------------------------------------------------------

// schedule is the victim's receive ports over time, sorted by start time.
type schedule struct {
	ms   []int64  // slot start, unix ms
	port []uint16 // the port active from ms[i] until ms[i+1]
}

// portAt returns the port the victim used at unix time t (the last slot that
// started at or before t; the first port if t precedes the schedule).
func (s schedule) portAt(t int64) uint16 {
	i := sort.Search(len(s.ms), func(i int) bool { return s.ms[i] > t }) - 1
	if i < 0 {
		i = 0
	}
	return s.port[i]
}

// loadSchedule waits up to 30 s for the file (the victim writes it after its
// handshake), then parses "<unix_ms> <port>" lines.
func loadSchedule(path string) (schedule, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		if f, err := os.Open(path); err == nil {
			s, perr := parseSchedule(f)
			f.Close()
			if perr == nil && len(s.ms) > 0 {
				return s, nil
			}
		}
		if time.Now().After(deadline) {
			return schedule{}, fmt.Errorf("no schedule at %s after 30s", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func parseSchedule(f *os.File) (schedule, error) {
	var s schedule
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ms int64
		var port uint16
		if _, err := fmt.Sscanf(sc.Text(), "%d %d", &ms, &port); err == nil {
			s.ms = append(s.ms, ms)
			s.port = append(s.port, port)
		}
	}
	return s, sc.Err()
}

// runReactive floods the victim's address on the port it used reactDelay ago,
// re-aiming as the schedule changes. The oracle attacker knows the port but
// reacts with a delay, so over a slot of length I it is on the right port for
// about I-reactDelay; once reactDelay >= I it is always a hop behind.
func runReactive(ip, schedulePath string, reactDelay time.Duration, mbit float64, size int, at, duration time.Duration) (result, error) {
	sched, err := loadSchedule(schedulePath)
	if err != nil {
		return result{}, err
	}
	if at > 0 {
		time.Sleep(at)
	}
	pps := mbit * 1e6 / float64(size*8)
	pkt := make([]byte, size)
	copy(pkt, "FLOOD")
	delayMs := reactDelay.Milliseconds()

	var conn *net.UDPConn
	var curPort uint16
	redial := func(port uint16) error {
		if conn != nil {
			conn.Close()
		}
		c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP(ip), Port: int(port)})
		if err != nil {
			return err
		}
		c.SetWriteBuffer(4 << 20)
		conn, curPort = c, port
		return nil
	}

	var packets, bytes int64
	start := time.Now()
	deadline := start.Add(duration)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for now := start; now.Before(deadline); now = <-tick.C {
		if port := sched.portAt(now.UnixMilli() - delayMs); conn == nil || port != curPort {
			if err := redial(port); err != nil {
				continue
			}
		}
		due := int64(math.Min(now.Sub(start).Seconds(), duration.Seconds()) * pps)
		for packets < due {
			if _, err := conn.Write(pkt); err == nil {
				bytes += int64(size)
			}
			packets++
		}
	}
	if conn != nil {
		conn.Close()
	}
	end := time.Now()
	return result{
		Target:      ip + " (reactive)",
		StartUnixMs: float64(start.UnixMicro()) / 1000,
		EndUnixMs:   float64(end.UnixMicro()) / 1000,
		Packets:     packets,
		Bytes:       bytes,
		Mbit:        mbit,
	}, nil
}
