// punch: hole punching and the peer-to-peer measurements, run on peer-a and
// peer-b. Raw UDP by default; -direct runs the Noise-encrypted, port-hopping
// direct channel over the same steps instead.
//
//	punch -role a -session 42      # on peer-a
//	punch -role b -session 42      # on peer-b, at the same time
//
//	punch -role a -session 42 -direct -hop-interval 1s   # direct channel
//
// Progress goes to stderr; the last line of stdout is one JSON object with
// every measurement, as the runner's "command" trial type expects.
// Exit status: 0 when the run completed (a punch that fails is a result,
// reported as "punched": false), 1 when the run itself broke.
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
	role := flag.String("role", "", "this peer's role: a or b (required)")
	flag.Uint64Var(&c.Session, "session", 0, "session ID shared by both peers, e.g. the trial seed (required)")
	flag.StringVar(&c.Local, "local", ":0", "local UDP address to bind")
	flag.StringVar(&c.STUN, "stun", "203.0.113.100:3478", `STUN server ("" to skip)`)
	flag.StringVar(&c.Rendezvous, "rendezvous", "203.0.113.101:7000", "rendezvous server")
	flag.DurationVar(&c.STUNTimeout, "stun-timeout", 2*time.Second, "give up on STUN after this long")
	flag.DurationVar(&c.RendezvousTimeout, "rendezvous-timeout", 15*time.Second, "wait this long for the other peer to register")
	flag.DurationVar(&c.PunchTimeout, "punch-timeout", 10*time.Second, "give up hole punching after this long")
	flag.DurationVar(&c.PunchInterval, "punch-interval", 50*time.Millisecond, "time between punch packets")
	flag.DurationVar(&c.StepTimeout, "step-timeout", 10*time.Second, "wait this long for the other peer between steps")
	flag.IntVar(&c.Pings, "pings", 50, "peer-to-peer pings (0 to skip)")
	flag.DurationVar(&c.PingInterval, "ping-interval", 100*time.Millisecond, "time between pings")
	flag.DurationVar(&c.PingGrace, "ping-grace", time.Second, "wait this long for late ping replies")
	flag.Float64Var(&c.RateMbit, "rate-mbit", 10, "throughput test: offered load per direction in Mbit/s (0 to skip)")
	flag.DurationVar(&c.Duration, "duration", 2*time.Second, "throughput test: duration per direction")
	flag.IntVar(&c.Size, "size", 1200, "throughput test: UDP payload bytes per packet")
	flag.DurationVar(&c.Drain, "drain", time.Second, "throughput test: wait for queued packets after each direction")
	flag.DurationVar(&c.Linger, "linger", 500*time.Millisecond, "keep answering the peer this long before exiting")

	flag.BoolVar(&c.Direct, "direct", false, "run the Noise-encrypted, port-hopping direct channel instead of raw UDP")
	pskHex := flag.String("psk", "", "direct: Noise pre-shared key, 64 hex digits, the same on both peers (default: derived from -session)")
	flag.DurationVar(&c.HandshakeTimeout, "handshake-timeout", 5*time.Second, "direct: give up on the Noise handshake after this long")
	flag.DurationVar(&c.HopInterval, "hop-interval", time.Second, "direct: how long each port is used")
	flag.DurationVar(&c.HopLead, "hop-lead", 0, "direct: open the next port this long before its slot (0: a tenth of -hop-interval)")
	flag.DurationVar(&c.HopMinOverlap, "hop-min-overlap", 0, "direct: keep the old port at least this long after its slot; the clock tolerance (0: a tenth of -hop-interval)")
	flag.DurationVar(&c.HopOverlap, "hop-overlap", 0, "direct: close the old port at the latest this long after its slot; it closes sooner once drained (0: half of -hop-interval)")
	flag.DurationVar(&c.HopDrainIdle, "hop-drain-idle", 0, "direct: the old port counts as drained after this long without packets, once traffic uses the new one (0: 50ms)")
	flag.BoolVar(&c.HopForce, "hop-force", false, "direct: hop even when a NAT changes ports (measures the failure)")
	flag.DurationVar(&c.ClockOffset, "clock-offset", 0, "direct: shift this peer's hop clock, e.g. 30ms or -30ms")
	flag.Float64Var(&c.ClockDriftPPM, "clock-drift-ppm", 0, "direct: make this peer's hop clock run fast (+) or slow (-), parts per million")
	quiet := flag.Bool("quiet", false, "no progress output on stderr")
	flag.Parse()

	sessionSet := false
	flag.Visit(func(f *flag.Flag) { sessionSet = sessionSet || f.Name == "session" })
	if len(*role) != 1 || !sessionSet {
		fmt.Fprintln(os.Stderr, "usage: punch -role a|b -session N [flags]")
		flag.PrintDefaults()
		os.Exit(2)
	}
	c.Role = (*role)[0]
	if c.Direct {
		psk, err := makePSK(*pskHex, c.Session)
		if err != nil {
			fmt.Fprintln(os.Stderr, "-psk:", err)
			os.Exit(2)
		}
		c.PSK = psk
	}
	if !*quiet {
		c.Logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}

	res, err := peer.Run(c)
	out, _ := json.Marshal(res)
	fmt.Println(string(out))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// makePSK decodes -psk, or derives a key from the session ID when -psk is
// empty. The derived key is a testbed convenience only: anyone who knows or
// guesses the session ID (the trial seed) knows the key. Attack experiments
// where the attacker must not be able to complete a handshake should pass a
// random -psk to both peers.
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

