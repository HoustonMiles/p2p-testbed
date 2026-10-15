// Package rendezvous is the introduction server for hole punching. Each peer
// sends Register (session, role, and its static public key if it has one)
// from the socket it will punch with; once both roles of a session have
// registered, every Register is answered by sending PeerInfo to BOTH peers:
// each one's public address as seen here, the other peer's, and the other
// peer's key.
//
// Peers that take the key from here trust this server not to swap it; with
// keys provisioned in advance (-peer-key FILE), they do not have to.
//
// The server never relays peer traffic. It keeps no state beyond the last
// address of each (session, role), which expires after TTL without a refresh,
// so a stale registration from an earlier trial is never handed out.
package rendezvous

import (
	"log"
	"net"
	"net/netip"
	"time"

	"p2ptestbed/p2p/internal/wire"
)

type entry struct {
	addr netip.AddrPort
	seen time.Time
	key  []byte // the peer's static public key; nil if it sent none
}

type Server struct {
	Conn *net.UDPConn
	TTL  time.Duration
	Log  *log.Logger

	sessions map[uint64]map[byte]entry
	paired   map[uint64]bool // sessions already logged as paired
}

func (s *Server) Serve() error {
	s.sessions = map[uint64]map[byte]entry{}
	s.paired = map[uint64]bool{}
	buf := make([]byte, 2048)
	lastGC := time.Now()
	for {
		n, from, err := s.Conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return err
		}
		now := time.Now()
		if now.Sub(lastGC) > 10*s.TTL {
			s.gc(now)
			lastGC = now
		}
		p, ok := wire.Parse(buf[:n])
		if !ok || p.Type != wire.Register || (p.Role != 'a' && p.Role != 'b') {
			continue
		}
		s.register(p, netip.AddrPortFrom(from.Addr().Unmap(), from.Port()), now)
	}
}

// register records the sender and, once both peers of its session are
// known, introduces them.
func (s *Server) register(p wire.Packet, from netip.AddrPort, now time.Time) {
	peers := s.sessions[p.Session]
	if peers == nil {
		peers = map[byte]entry{}
		s.sessions[p.Session] = peers
	}
	peers[p.Role] = entry{from, now, registeredKey(p.Body)}
	other, ok := peers[p.Role^'a'^'b'] // 'a' <-> 'b'
	if !ok || now.Sub(other.seen) > s.TTL {
		return // the other peer hasn't registered (recently) yet
	}
	// Introduce both peers now, not only the sender: the other would learn
	// of the pairing only at its next Register, adding the registration
	// interval to the measured punch time.
	s.introduce(p.Session, p.Role, from, other)
	s.introduce(p.Session, p.Role^'a'^'b', other.addr, peers[p.Role])
	if !s.paired[p.Session] {
		s.paired[p.Session] = true
		s.logf("session %d: %c=%v %c=%v", p.Session, p.Role, from, p.Role^'a'^'b', other.addr)
	}
}

// registeredKey copies the key out of a Register body (the read buffer is
// reused); a body of any other length carries no key.
func registeredKey(body []byte) []byte {
	if len(body) != wire.KeySize {
		return nil
	}
	return append([]byte(nil), body...)
}

// introduce tells the peer at self where the other peer is, and its key.
func (s *Server) introduce(session uint64, role byte, self netip.AddrPort, peer entry) {
	body := append(wire.Addrs(self, peer.addr), peer.key...)
	p := wire.Packet{Type: wire.PeerInfo, Role: role, Session: session, Body: body}
	if _, err := s.Conn.WriteToUDPAddrPort(p.Append(nil), self); err != nil {
		s.logf("introduce %v: %v", self, err)
	}
}

func (s *Server) gc(now time.Time) {
	for id, peers := range s.sessions {
		for role, e := range peers {
			if now.Sub(e.seen) > s.TTL {
				delete(peers, role)
			}
		}
		if len(peers) == 0 {
			delete(s.sessions, id)
			delete(s.paired, id)
		}
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log.Printf(format, args...)
	}
}
