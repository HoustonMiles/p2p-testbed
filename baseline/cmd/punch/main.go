// punch: the raw UDP hole-punching baseline, run on peer-a and peer-b.
//
//	punch -role a -session 42      # on peer-a
//	punch -role b -session 42      # on peer-b, at the same time
//
// Progress goes to stderr; the last line of stdout is one JSON object with
// every measurement, as the runner's "command" trial type expects.
// Exit status: 0 when the run completed (a punch that fails is a result,
// reported as "punched": false), 1 when the run itself broke.
package main

import (
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
