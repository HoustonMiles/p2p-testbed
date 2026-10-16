// Package wire is the packet format shared by the punch tool and the
// rendezvous server. It is deliberately plain: no encryption, no
// authentication, so the baseline measures only what UDP and the NATs cost.
// The direct channel (package direct) sends the same packets, but encrypted
// inside its own transport; only its Handshake messages travel in the clear.
//
// Every packet starts with the same 32-byte header (big-endian):
//
//	0        4      5         8            16           24          32
//	| "P2PB" | type | 0  0  0 | room (u64) | from (u64) | seq (u64) | body ...
//
// room groups the peers that should meet (in the testbed, one trial), from
// is the sender's peer ID (0 for the rendezvous server), and seq is a
// per-type counter (attempt, ping number, step...).
package wire

import (
	"encoding/binary"
	"net/netip"
)

const (
	Magic     = "P2PB"
	HeaderLen = 32
)

type Type uint8

const (
	Register   Type = iota + 1 // peer -> rendezvous: "I am <from> in <room>"; body: our static public key, if any
	PeerInfo                   // rendezvous -> peer: an Intro of one other member of the room
	Punch                      // peer -> peer: hole-punching probe
	PunchAck                   // reply to a Punch: proves the path works both ways
	Ping                       // body: sender's timestamp (ns), echoed back in the Pong
	Pong                       // reply to a Ping
	Barrier                    // "I have reached step <seq>"; body: a value (e.g. packets sent)
	BarrierAck                 // reply to a Barrier from a peer that has reached that step too
	Data                       // throughput test payload (padding)
	Handshake                  // peer -> peer: a Noise handshake message (direct channel only)
	PeerLeft                   // rendezvous -> peer: a member's registration expired; body: its ID (u64)
)

type Packet struct {
	Type Type
	Room uint64
	From uint64
	Seq  uint64
	Body []byte
}

// Append encodes p onto buf and returns the extended slice.
func (p Packet) Append(buf []byte) []byte {
	buf = append(buf, Magic...)
	buf = append(buf, byte(p.Type), 0, 0, 0)
	buf = binary.BigEndian.AppendUint64(buf, p.Room)
	buf = binary.BigEndian.AppendUint64(buf, p.From)
	buf = binary.BigEndian.AppendUint64(buf, p.Seq)
	return append(buf, p.Body...)
}

// Parse decodes a packet. Body aliases b, so copy it if b is reused. The
// reserved bytes must be zero, so every accepted packet has exactly one
// encoding (found by FuzzParse).
func Parse(b []byte) (Packet, bool) {
	if len(b) < HeaderLen || string(b[:4]) != Magic || b[5] != 0 || b[6] != 0 || b[7] != 0 {
		return Packet{}, false
	}
	return Packet{
		Type: Type(b[4]),
		Room: binary.BigEndian.Uint64(b[8:16]),
		From: binary.BigEndian.Uint64(b[16:24]),
		Seq:  binary.BigEndian.Uint64(b[24:32]),
		Body: b[HeaderLen:],
	}, true
}

// PutSeq rewrites the sequence number of an encoded packet in place, so a
// stream of packets can reuse one buffer.
func PutSeq(pkt []byte, seq uint64) { binary.BigEndian.PutUint64(pkt[24:32], seq) }

// U64 / ParseU64: the single-number bodies of Ping, Pong, Barrier and
// PeerLeft.
func U64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

func ParseU64(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}

// KeySize is the length of a static public key carried by Register and
// PeerInfo.
const KeySize = 32

// Intro is the PeerInfo body: the receiver's own public address as the
// rendezvous sees it, and one other member of the room.
//
//	self (IPv4, port) | peer (IPv4, port) | peer ID (u64) | peer key (32 bytes, optional)
//
// TODO(paper §5 Simulation Environment, real networks): IPv4 only; add
// IPv6 addresses before running between people's own machines.
type Intro struct {
	Self, Peer netip.AddrPort
	PeerID     uint64
	PeerKey    []byte // nil when that peer registered without one
}

const introLen = 6 + 6 + 8

func (in Intro) Append(buf []byte) []byte {
	for _, a := range []netip.AddrPort{in.Self, in.Peer} {
		ip := a.Addr().Unmap().As4()
		buf = append(buf, ip[:]...)
		buf = binary.BigEndian.AppendUint16(buf, a.Port())
	}
	buf = binary.BigEndian.AppendUint64(buf, in.PeerID)
	return append(buf, in.PeerKey...)
}

// ParseIntro decodes a PeerInfo body. PeerKey aliases b. A key of any
// length other than KeySize is ignored.
func ParseIntro(b []byte) (Intro, bool) {
	if len(b) < introLen {
		return Intro{}, false
	}
	get := func(b []byte) netip.AddrPort {
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[:4])), binary.BigEndian.Uint16(b[4:6]))
	}
	in := Intro{Self: get(b[0:6]), Peer: get(b[6:12]), PeerID: binary.BigEndian.Uint64(b[12:20])}
	if len(b) == introLen+KeySize {
		in.PeerKey = b[introLen:]
	}
	return in, true
}
