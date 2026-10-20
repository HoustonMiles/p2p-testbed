// Package stun is the smallest useful STUN client (RFC 8489): a Binding
// request, and the XOR-MAPPED-ADDRESS (or legacy MAPPED-ADDRESS) from the
// response, which is all a peer needs to learn its public address; and the
// message format with MESSAGE-INTEGRITY (message.go), which TURN builds on.
package stun

import (
	"crypto/rand"
	"encoding/binary"
	"net/netip"
)

const (
	headerLen = 20
	cookie    = 0x2112A442

	bindingRequest = 0x0001
	bindingSuccess = 0x0101

	attrMappedAddress    = 0x0001
	attrXORMappedAddress = 0x0020
)

type TxID [12]byte

func NewTxID() TxID {
	var id TxID
	rand.Read(id[:])
	return id
}

// IsMessage reports whether b looks like a STUN message (RFC 8489 section 5:
// first two bits zero and the magic cookie in place). Lets STUN replies share
// a socket with other traffic.
func IsMessage(b []byte) bool {
	return len(b) >= headerLen && b[0]&0xC0 == 0 &&
		binary.BigEndian.Uint32(b[4:8]) == cookie
}

// Request builds a Binding request with no attributes.
func Request(id TxID) []byte { return Message{Type: bindingRequest, ID: id}.Encode() }

// Response builds a Binding success response carrying XOR-MAPPED-ADDRESS.
// The tool doesn't need it; it is here for tests.
func Response(id TxID, mapped netip.AddrPort) []byte {
	return Message{Type: bindingSuccess, ID: id, Attrs: []Attr{{attrXORMappedAddress, XORAddress(mapped)}}}.Encode()
}

// Parse returns the mapped address from a Binding success response with the
// given transaction ID, preferring XOR-MAPPED-ADDRESS over the legacy
// MAPPED-ADDRESS.
func Parse(b []byte, id TxID) (netip.AddrPort, bool) {
	m, ok := Decode(b)
	if !ok || m.Type != bindingSuccess || m.ID != id {
		return netip.AddrPort{}, false
	}
	if v, ok := m.Get(attrXORMappedAddress); ok {
		if a, ok := ParseXORAddress(v); ok {
			return a, true
		}
	}
	if v, ok := m.Get(attrMappedAddress); ok {
		if a, ok := ipv4Attr(v); ok {
			return addr(a.ip, a.port), true
		}
	}
	return netip.AddrPort{}, false
}

type rawAddr struct {
	ip   uint32
	port uint16
}

// ipv4Attr decodes an address attribute: reserved, family 0x01, port, IP.
func ipv4Attr(val []byte) (rawAddr, bool) {
	if len(val) < 8 || val[1] != 0x01 {
		return rawAddr{}, false
	}
	return rawAddr{ip: binary.BigEndian.Uint32(val[4:8]), port: binary.BigEndian.Uint16(val[2:4])}, true
}

func addr(ip uint32, port uint16) netip.AddrPort {
	var a [4]byte
	binary.BigEndian.PutUint32(a[:], ip)
	return netip.AddrPortFrom(netip.AddrFrom4(a), port)
}
