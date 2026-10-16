// Package rendezvous is the introduction server for hole punching. Each peer
// sends Register (room, its peer ID, and its static public key if it has
// one) from the socket it will punch with. Every Register is answered with
// one PeerInfo per other member of the room, and each of those members gets
// a PeerInfo about the sender: each one's public address as seen here, and
// the other's ID and key. A full mesh forms as every pair punches.
//
// Peers that take keys from here trust this server not to swap them; with
// keys provisioned in advance (-peer-key FILE or DIR), they do not have to.
//
// The server never relays peer traffic. It keeps no state beyond the last
// address of each member, which expires after TTL without a refresh; the
// rest of the room is then sent PeerLeft, and a stale registration from an
// earlier trial is never handed out.
//
// TODO(paper §5 Threat Model): anyone who knows a room number can join it
// and learn the members' addresses. Authenticate Register (e.g. a MAC under
// a room key) and cap the room size before running outside the testbed.
package rendezvous

import (
	"errors"
	"log"
	"net"
	"net/netip"
	"os"
	"time"

	"p2ptestbed/p2p/internal/wire"
)

type member struct {
	addr netip.AddrPort
	seen time.Time
	key  []byte // the member's static public key; nil if it sent none
}

type room map[uint64]member // by peer ID

type Server struct {
	Conn *net.UDPConn
	TTL  time.Duration
	Log  *log.Logger

	rooms      map[uint64]room
	lastExpire time.Time
}

func (s *Server) Serve() error {
	s.rooms = map[uint64]room{}
	buf := make([]byte, 2048)
	for {
		// The deadline wakes the loop to expire members even when no
		// packets arrive.
		s.Conn.SetReadDeadline(time.Now().Add(s.TTL / 4))
		n, from, err := s.Conn.ReadFromUDPAddrPort(buf)
		now := time.Now()
		s.maybeExpire(now)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			continue
		}
		if err != nil {
			return err
		}
		p, ok := wire.Parse(buf[:n])
		if ok && p.Type == wire.Register && p.From != 0 {
			s.register(p, netip.AddrPortFrom(from.Addr().Unmap(), from.Port()), now)
		}
	}
}

// register records the sender and introduces it to every live member of its
// room, both ways. Introducing the other members now, not only the sender,
// keeps the registration interval out of the measured punch time.
func (s *Server) register(p wire.Packet, from netip.AddrPort, now time.Time) {
	r := s.rooms[p.Room]
	if r == nil {
		r = room{}
		s.rooms[p.Room] = r
	}
	s.expire(p.Room, r, now) // so no stale member is introduced
	_, known := r[p.From]
	self := member{from, now, registeredKey(p.Body)}
	r[p.From] = self
	if !known {
		s.logf("room %d: %016x joined from %v (%d members)", p.Room, p.From, from, len(r))
	}
	for id, m := range r {
		if id == p.From {
			continue
		}
		s.introduce(p.Room, self.addr, id, m)
		s.introduce(p.Room, m.addr, p.From, self)
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

// introduce tells the member at to where member id is, and its key.
func (s *Server) introduce(roomID uint64, to netip.AddrPort, id uint64, m member) {
	body := wire.Intro{Self: to, Peer: m.addr, PeerID: id, PeerKey: m.key}.Append(nil)
	s.send(to, wire.Packet{Type: wire.PeerInfo, Room: roomID, Body: body})
}

func (s *Server) maybeExpire(now time.Time) {
	if now.Sub(s.lastExpire) < s.TTL/4 {
		return
	}
	s.lastExpire = now
	for id, r := range s.rooms {
		s.expire(id, r, now)
		if len(r) == 0 {
			delete(s.rooms, id)
		}
	}
}

// expire drops members not refreshed within TTL and tells the rest.
func (s *Server) expire(roomID uint64, r room, now time.Time) {
	for id, m := range r {
		if now.Sub(m.seen) <= s.TTL {
			continue
		}
		delete(r, id)
		s.logf("room %d: %016x left (%d members)", roomID, id, len(r))
		for _, other := range r {
			s.send(other.addr, wire.Packet{Type: wire.PeerLeft, Room: roomID, Body: wire.U64(id)})
		}
	}
}

func (s *Server) send(to netip.AddrPort, p wire.Packet) {
	if _, err := s.Conn.WriteToUDPAddrPort(p.Append(nil), to); err != nil {
		s.logf("send to %v: %v", to, err)
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log.Printf(format, args...)
	}
}
