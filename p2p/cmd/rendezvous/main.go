// rendezvous: introduces the members of each room to each other.
// Runs on the WAN at 203.0.113.101 (see docker-compose.yml).
package main

import (
	"flag"
	"log"
	"net"
	"os"
	"time"

	"p2ptestbed/p2p/internal/rendezvous"
)

func main() {
	listen := flag.String("listen", ":7000", "UDP address to listen on")
	ttl := flag.Duration("ttl", 5*time.Second, "forget a registration not refreshed for this long")
	flag.Parse()

	addr, err := net.ResolveUDPAddr("udp4", *listen)
	if err != nil {
		log.Fatal(err)
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		log.Fatal(err)
	}
	logger := log.New(os.Stderr, "[rendezvous] ", log.LstdFlags)
	logger.Printf("listening on %v", conn.LocalAddr())
	s := &rendezvous.Server{Conn: conn, TTL: *ttl, Log: logger}
	log.Fatal(s.Serve())
}
