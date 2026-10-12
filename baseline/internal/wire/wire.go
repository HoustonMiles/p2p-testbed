// Package wire is the packet format shared by the punch tool and the
// rendezvous server. It is deliberately plain: no encryption, no
// authentication, so the baseline measures only what UDP and the NATs cost.
//
// Every packet starts with the same 24-byte header (big-endian):
//
//	0        4      5      6       8               16          24
//	| "P2PB" | type | role | 0  0  | session (u64) | seq (u64) | body ...
//
// role is 'a' or 'b' (the sender), session ties both peers of one trial
// together, and seq is a per-type counter (attempt, ping number, step...).
package wire

import (
	"encoding/binary"
	"net/netip"
)

const (
	Magic     = "P2PB"
	HeaderLen = 24
)

type Type uint8

const (
	Register   Type = iota + 1 // peer -> rendezvous: "I am <role> of <session>"
	PeerInfo                   // rendezvous -> peer: your public address and the other peer's
	Punch                      // peer -> peer: hole-punching probe
	PunchAck                   // reply to a Punch: proves the path works both ways
	Ping                       // body: sender's timestamp (ns), echoed back in the Pong
	Pong                       // reply to a Ping
	Barrier                    // "I have reached step <seq>"; body: a value (e.g. packets sent)
	BarrierAck                 // reply to a Barrier from a peer that has reached that step too
	Data                       // throughput test payload (padding)
)

type Packet struct {
	Type    Type
	Role    byte
	Session uint64
	Seq     uint64
	Body    []byte
}

// Append encodes p onto buf and returns the extended slice.
func (p Packet) Append(buf []byte) []byte {
	buf = append(buf, Magic...)
	buf = append(buf, byte(p.Type), p.Role, 0, 0)
	buf = binary.BigEndian.AppendUint64(buf, p.Session)
	buf = binary.BigEndian.AppendUint64(buf, p.Seq)
	return append(buf, p.Body...)
}

// Parse decodes a packet. Body aliases b, so copy it if b is reused.
func Parse(b []byte) (Packet, bool) {
	if len(b) < HeaderLen || string(b[:4]) != Magic {
		return Packet{}, false
	}
	return Packet{
		Type:    Type(b[4]),
		Role:    b[5],
		Session: binary.BigEndian.Uint64(b[8:16]),
		Seq:     binary.BigEndian.Uint64(b[16:24]),
		Body:    b[HeaderLen:],
	}, true
}

// U64 / ParseU64: the single-number bodies of Ping, Pong and Barrier.
func U64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

func ParseU64(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}

// Addrs / ParseAddrs: the PeerInfo body, two IPv4 address:port pairs
// (6 bytes each): the receiver's own public address, then the other peer's.
func Addrs(self, peer netip.AddrPort) []byte {
	b := make([]byte, 0, 12)
	for _, a := range []netip.AddrPort{self, peer} {
		ip := a.Addr().Unmap().As4()
		b = append(b, ip[:]...)
		b = binary.BigEndian.AppendUint16(b, a.Port())
	}
	return b
}

func ParseAddrs(b []byte) (self, peer netip.AddrPort, ok bool) {
	if len(b) < 12 {
		return self, peer, false
	}
	get := func(b []byte) netip.AddrPort {
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[:4])), binary.BigEndian.Uint16(b[4:6]))
	}
	return get(b[0:6]), get(b[6:12]), true
}
