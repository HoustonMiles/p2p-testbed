// punch runs one side of a peer-to-peer trial on peer-a or peer-b: raw UDP
// by default, the Port-Hopping Direct Channel with -direct.
//
//	punch -role a -session 42      # on peer-a
//	punch -role b -session 42      # on peer-b, at the same time
//	punch -role a -session 42 -direct -hop-interval 1s
//
// Progress goes to stderr; the last line of stdout is one JSON object with
// every measurement, as the runner's "command" trial type expects. Exit
// status 0 means the run completed (a failed punch or hop is a result),
// 1 that the run itself broke, 2 a usage error.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"p2ptestbed/baseline/internal/peer"
)

func main() {
	var c peer.Config
	var d peer.DirectConfig
	role := flag.String("role", "", "this peer's role: a or b (required)")
	connectFlags(&c)
	measureFlags(&c)
	useDirect := flag.Bool("direct", false, "run the Port-Hopping Direct Channel instead of raw UDP")
	pskHex := flag.String("psk", "", "direct: Noise pre-shared key, 64 hex digits, the same on both peers (default: derived from -session)")
	directFlags(&d)
	quiet := flag.Bool("quiet", false, "no progress output on stderr")
	flag.Parse()

	if len(*role) != 1 || !flagSet("session") {
		usage("usage: punch -role a|b -session N [flags]")
	}
	c.Role = (*role)[0]
	if *useDirect {
		psk, err := makePSK(*pskHex, c.Session)
		if err != nil {
			usage("-psk: " + err.Error())
		}
		d.PSK = psk
		c.Direct = &d
	}
	if !*quiet {
		c.Logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}
	os.Exit(run(c))
}

func run(c peer.Config) int {
	res, err := peer.Run(c)
	out, _ := json.Marshal(res)
	fmt.Println(string(out))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// TODO(paper §5 Simulation Environment): these addresses mirror
// docker-compose.yml; read them from the environment so they are defined once.
func connectFlags(c *peer.Config) {
	flag.Uint64Var(&c.Session, "session", 0, "session ID shared by both peers, e.g. the trial seed (required)")
	flag.StringVar(&c.Local, "local", ":0", "local UDP address to bind")
	flag.StringVar(&c.STUN, "stun", "203.0.113.100:3478", `STUN server ("" to skip)`)
	flag.StringVar(&c.Rendezvous, "rendezvous", "203.0.113.101:7000", "rendezvous server")
	flag.DurationVar(&c.STUNTimeout, "stun-timeout", 2*time.Second, "give up on STUN after this long")
	flag.DurationVar(&c.RendezvousTimeout, "rendezvous-timeout", 15*time.Second, "wait this long for the other peer to register")
	flag.DurationVar(&c.PunchTimeout, "punch-timeout", 10*time.Second, "give up hole punching after this long")
	flag.DurationVar(&c.PunchInterval, "punch-interval", 50*time.Millisecond, "time between punch packets")
	flag.DurationVar(&c.StepTimeout, "step-timeout", 10*time.Second, "wait this long for the other peer between steps")
	flag.DurationVar(&c.Linger, "linger", 500*time.Millisecond, "keep answering the peer this long before exiting")
}

func measureFlags(c *peer.Config) {
	flag.IntVar(&c.Pings, "pings", 50, "peer-to-peer pings (0 to skip)")
	flag.DurationVar(&c.PingInterval, "ping-interval", 100*time.Millisecond, "time between pings")
	flag.DurationVar(&c.PingGrace, "ping-grace", time.Second, "wait this long for late ping replies")
	flag.Float64Var(&c.RateMbit, "rate-mbit", 10, "throughput test: offered load per direction in Mbit/s (0 to skip)")
	flag.DurationVar(&c.Duration, "duration", 2*time.Second, "throughput test: duration per direction")
	flag.IntVar(&c.Size, "size", 1200, "throughput test: UDP payload bytes per packet")
	flag.DurationVar(&c.Drain, "drain", time.Second, "throughput test: wait for queued packets after each direction")
}

func directFlags(d *peer.DirectConfig) {
	flag.DurationVar(&d.HandshakeTimeout, "handshake-timeout", 5*time.Second, "direct: give up on the Noise handshake after this long")
	flag.DurationVar(&d.HopInterval, "hop-interval", time.Second, "direct: how long each port is used")
	flag.DurationVar(&d.HopLead, "hop-lead", 0, "direct: open the next port this long before its slot (0: a tenth of -hop-interval)")
	flag.DurationVar(&d.HopMinOverlap, "hop-min-overlap", 0, "direct: keep the old port at least this long after its slot; the clock tolerance (0: a tenth of -hop-interval)")
	flag.DurationVar(&d.HopMaxOverlap, "hop-overlap", 0, "direct: close the old port at the latest this long after its slot; it closes sooner once drained (0: half of -hop-interval)")
	flag.DurationVar(&d.HopDrainIdle, "hop-drain-idle", 0, "direct: the old port counts as drained after this long without packets, once traffic uses the new one (0: 50ms)")
	flag.BoolVar(&d.HopForce, "hop-force", false, "direct: hop even when a NAT changes ports (measures the failure)")
	flag.DurationVar(&d.ClockOffset, "clock-offset", 0, "direct: shift this peer's hop clock, e.g. 30ms or -30ms")
	flag.Float64Var(&d.ClockDriftPPM, "clock-drift-ppm", 0, "direct: make this peer's hop clock run fast (+) or slow (-), parts per million")
}

func flagSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

func usage(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	flag.PrintDefaults()
	os.Exit(2)
}

// makePSK decodes -psk, or derives one from the session ID. The derived key
// is a testbed convenience: anyone who guesses the session ID (the trial
// seed) knows it.
//
// TODO(paper §5 Threat Model): attack experiments must pass a random -psk,
// or switch to static keys (see the handshake TODO in package direct).
func makePSK(hexKey string, session uint64) ([]byte, error) {
	if hexKey == "" {
		sum := sha256.Sum256(fmt.Appendf(nil, "p2ptestbed psk %d", session))
		return sum[:], nil
	}
	psk, err := hex.DecodeString(hexKey)
	if err != nil || len(psk) != 32 {
		return nil, fmt.Errorf("want 64 hex digits (32 bytes)")
	}
	return psk, nil
}

