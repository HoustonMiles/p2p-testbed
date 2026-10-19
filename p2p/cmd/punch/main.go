// punch runs one peer of a peer-to-peer trial: raw UDP by default, the
// Port-Hopping Direct Channel with -direct. Peers meet in a room at the
// rendezvous and link with every other member (a full mesh).
//
//	punch -name a -room 42         # on peer-a
//	punch -name b -room 42         # on peer-b, at the same time
//	punch -name a -room 42 -direct -hop-interval 1s
//	punch -name a -room 42 -peers 3   # a room of three: links to both others
//	punch -genkey /keys/a          # write /keys/a.key and /keys/a.pub, then exit
//
// Progress goes to stderr; stdout gets one JSON object per link with every
// measurement, so with two peers the last (only) line is what the runner's
// "command" trial type expects. Exit status 0 means the run completed (a
// failed punch or hop is a result), 1 that the run or a link broke, 2 a
// usage error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"p2ptestbed/p2p/internal/identity"
	"p2ptestbed/p2p/internal/peer"
)

func main() {
	var c peer.Config
	var d peer.DirectConfig
	flag.StringVar(&c.Name, "name", "", "this peer's name, for logs, results and the default key file (required)")
	connectFlags(&c)
	measureFlags(&c)
	useDirect := flag.Bool("direct", false, "run the Port-Hopping Direct Channel instead of raw UDP")
	directFlags(&d)
	keys := keyFlags()
	genkey := flag.String("genkey", "", "write a new static keypair to `BASE`.key and BASE.pub, then exit")
	quiet := flag.Bool("quiet", false, "no progress output on stderr")
	flag.Parse()

	if *genkey != "" {
		os.Exit(writeKeypair(*genkey))
	}
	if c.Name == "" || !flagSet("room") {
		usage("usage: punch -name NAME -room N [flags]")
	}
	if *useDirect {
		if err := keys.load(&d, c.Name); err != nil {
			usage(err.Error())
		}
		c.Direct = &d
	}
	if !*quiet {
		c.Logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}
	os.Exit(run(c))
}

func run(c peer.Config) int {
	rows, err := peer.Run(c)
	status := 0
	for _, row := range rows {
		out, _ := json.Marshal(row)
		fmt.Println(string(out))
		if row.Error != "" {
			fmt.Fprintf(os.Stderr, "error (link %s): %s\n", row.PeerID, row.Error)
			status = 1
		}
	}
	if err != nil {
		status = 1
	}
	return status
}

// TODO(paper §5 Simulation Environment): these addresses mirror
// p2prunner/topology.py; pass them in from the compose file's environment
// so they are defined once.
func connectFlags(c *peer.Config) {
	flag.Uint64Var(&c.Room, "room", 0, "rendezvous room shared by the peers that should meet, e.g. the trial seed (required)")
	flag.IntVar(&c.Peers, "peers", 2, "members of the room, this peer included; links are made with all the others")
	flag.StringVar(&c.Local, "local", ":0", "local UDP address to bind")
	flag.StringVar(&c.STUN, "stun", "198.18.0.100:3478", `STUN server ("" to skip)`)
	flag.StringVar(&c.Rendezvous, "rendezvous", "198.18.0.101:7000", "rendezvous server")
	flag.DurationVar(&c.STUNTimeout, "stun-timeout", 2*time.Second, "give up on STUN after this long")
	flag.DurationVar(&c.RendezvousTimeout, "rendezvous-timeout", 15*time.Second, "wait this long for the other members of the room")
	flag.DurationVar(&c.PunchTimeout, "punch-timeout", 10*time.Second, "give up hole punching after this long")
	flag.DurationVar(&c.PunchInterval, "punch-interval", 50*time.Millisecond, "time between punch packets")
	flag.DurationVar(&c.StepTimeout, "step-timeout", 10*time.Second, "wait this long for a member between steps (in rooms of 3+, plus the punch and handshake timeouts)")
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
	flag.StringVar(&d.Noise, "noise", "kk", "direct: handshake pattern: kk (peers know each other's keys), ik (initiator knows the responder's), xx (first contact)")
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

type keySettings struct{ key, peerKey *string }

func keyFlags() keySettings {
	return keySettings{
		key:     flag.String("key", "", "direct: this peer's static key file (default /keys/<name>.key)"),
		peerKey: flag.String("peer-key", "/keys", `direct: the trusted peers' public keys: a .pub file or a directory of them; or "rendezvous" to take the key the rendezvous announces, or "none" to trust it on first use (xx, ik responder)`),
	}
}

// load reads the key files into d. Make them with -genkey.
func (k keySettings) load(d *peer.DirectConfig, name string) error {
	keyPath := orDefault(*k.key, fmt.Sprintf("/keys/%s.key", name))
	kp, err := identity.ReadKeypair(keyPath)
	if err != nil {
		return fmt.Errorf("-key: %v (make one with: punch -genkey /keys/%s)", err, name)
	}
	d.Key = kp
	switch from := *k.peerKey; from {
	case "rendezvous", "none":
		d.PeerKeyFrom = from
	default:
		d.PeerKeyFrom = "file"
		if d.TrustedKeys, err = identity.ReadTrustedKeys(from, kp.Public); err != nil {
			return fmt.Errorf("-peer-key: %v", err)
		}
	}
	return nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func writeKeypair(base string) int {
	kp, err := identity.NewKeypair()
	if err == nil {
		err = identity.WriteKeypair(base, kp)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "genkey:", err)
		return 1
	}
	fmt.Printf("wrote %s.key and %s.pub\n", base, base)
	return 0
}
