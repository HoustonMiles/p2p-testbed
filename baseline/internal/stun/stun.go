// Package stun is the smallest useful STUN client (RFC 8489): a Binding
// request, and the XOR-MAPPED-ADDRESS (or legacy MAPPED-ADDRESS) from the
// response. That is all a peer needs to learn its public address.
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
func Request(id TxID) []byte {
	b := make([]byte, headerLen)
	binary.BigEndian.PutUint16(b[0:2], bindingRequest)
	binary.BigEndian.PutUint32(b[4:8], cookie)
	copy(b[8:20], id[:])
	return b
}

// Response builds a Binding success response carrying XOR-MAPPED-ADDRESS.
// The tool doesn't need it; it is here for tests.
func Response(id TxID, mapped netip.AddrPort) []byte {
	b := make([]byte, headerLen, headerLen+12)
	binary.BigEndian.PutUint16(b[0:2], bindingSuccess)
	binary.BigEndian.PutUint16(b[2:4], 12)
	binary.BigEndian.PutUint32(b[4:8], cookie)
	copy(b[8:20], id[:])
	ip := mapped.Addr().Unmap().As4()
	b = binary.BigEndian.AppendUint16(b, attrXORMappedAddress)
	b = binary.BigEndian.AppendUint16(b, 8)
	b = append(b, 0, 0x01) // reserved, family IPv4
	b = binary.BigEndian.AppendUint16(b, mapped.Port()^(cookie>>16))
	return binary.BigEndian.AppendUint32(b, binary.BigEndian.Uint32(ip[:])^cookie)
}

// Parse returns the mapped address from a Binding success response with the
// given transaction ID.
func Parse(b []byte, id TxID) (netip.AddrPort, bool) {
	if !IsMessage(b) || binary.BigEndian.Uint16(b[0:2]) != bindingSuccess ||
		TxID(b[8:20]) != id {
		return netip.AddrPort{}, false
	}
	end := headerLen + int(binary.BigEndian.Uint16(b[2:4]))
	if end > len(b) {
		return netip.AddrPort{}, false
	}
	var legacy netip.AddrPort
	for off := headerLen; off+4 <= end; {
		typ := binary.BigEndian.Uint16(b[off:])
		n := int(binary.BigEndian.Uint16(b[off+2:]))
		val := b[off+4:]
		if off+4+n > end {
			break
		}
		// IPv4 address attributes: reserved, family 0x01, port, 4-byte IP.
		if n >= 8 && val[1] == 0x01 {
			port := binary.BigEndian.Uint16(val[2:4])
			ip := binary.BigEndian.Uint32(val[4:8])
			switch typ {
			case attrXORMappedAddress:
				return addr(ip^cookie, port^(cookie>>16)), true
			case attrMappedAddress:
				legacy = addr(ip, port)
			}
		}
		off += 4 + (n+3)&^3 // attributes are padded to 4 bytes
	}
	return legacy, legacy.IsValid()
}

func addr(ip uint32, port uint16) netip.AddrPort {
	var a [4]byte
	binary.BigEndian.PutUint32(a[:], ip)
	return netip.AddrPortFrom(netip.AddrFrom4(a), port)
}

