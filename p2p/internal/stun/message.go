package stun

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"net/netip"
)

// AttrMessageIntegrity is HMAC-SHA1 over the message before it (RFC 8489
// section 14.5); TURN's long-term credentials sign requests with it.
const AttrMessageIntegrity = 0x0008

const integrityLen = 4 + sha1.Size

// Attr is one attribute: its type and value, without padding.
type Attr struct {
	Type  uint16
	Value []byte
}

// Message is a STUN message: the type (method and class), the transaction
// ID and the attributes in order. TURN (package turn) is built on it.
type Message struct {
	Type  uint16
	ID    TxID
	Attrs []Attr
}

// Encode returns the message on the wire, each attribute padded to 4 bytes.
func (m Message) Encode() []byte {
	b := make([]byte, headerLen, 128)
	binary.BigEndian.PutUint16(b[0:2], m.Type)
	binary.BigEndian.PutUint32(b[4:8], cookie)
	copy(b[8:20], m.ID[:])
	for _, a := range m.Attrs {
		b = binary.BigEndian.AppendUint16(b, a.Type)
		b = binary.BigEndian.AppendUint16(b, uint16(len(a.Value)))
		b = append(b, a.Value...)
		b = append(b, make([]byte, pad(len(a.Value)))...)
	}
	binary.BigEndian.PutUint16(b[2:4], uint16(len(b)-headerLen))
	return b
}

// Decode parses a STUN message; ok is false if b is not one, or an
// attribute runs past its end.
func Decode(b []byte) (m Message, ok bool) {
	if !IsMessage(b) {
		return m, false
	}
	end := headerLen + int(binary.BigEndian.Uint16(b[2:4]))
	if end > len(b) {
		return m, false
	}
	m.Type, m.ID = binary.BigEndian.Uint16(b[0:2]), TxID(b[8:20])
	for off := headerLen; off < end; {
		if off+4 > end {
			return m, false
		}
		typ, n := binary.BigEndian.Uint16(b[off:]), int(binary.BigEndian.Uint16(b[off+2:]))
		if off+4+n > end {
			return m, false
		}
		m.Attrs = append(m.Attrs, Attr{typ, b[off+4 : off+4+n]})
		off += 4 + n + pad(n)
	}
	return m, true
}

// Get returns the first attribute of the given type.
func (m Message) Get(typ uint16) ([]byte, bool) {
	for _, a := range m.Attrs {
		if a.Type == typ {
			return a.Value, true
		}
	}
	return nil, false
}

// XORAddress is the value of an XOR-*-ADDRESS attribute for an IPv4 address.
func XORAddress(a netip.AddrPort) []byte {
	ip := a.Addr().Unmap().As4()
	v := []byte{0, 0x01} // reserved, family IPv4
	v = binary.BigEndian.AppendUint16(v, a.Port()^(cookie>>16))
	return binary.BigEndian.AppendUint32(v, binary.BigEndian.Uint32(ip[:])^cookie)
}

// ParseXORAddress decodes an IPv4 XOR-*-ADDRESS value.
func ParseXORAddress(v []byte) (netip.AddrPort, bool) {
	a, ok := ipv4Attr(v)
	if !ok {
		return netip.AddrPort{}, false
	}
	return addr(a.ip^cookie, a.port^(cookie>>16)), true
}

// AppendIntegrity signs an encoded message: it appends MESSAGE-INTEGRITY,
// computed with the length field already counting it, as the RFC requires.
func AppendIntegrity(b, key []byte) []byte {
	binary.BigEndian.PutUint16(b[2:4], uint16(len(b)-headerLen+integrityLen))
	mac := hmac.New(sha1.New, key)
	mac.Write(b)
	b = binary.BigEndian.AppendUint16(b, AttrMessageIntegrity)
	b = binary.BigEndian.AppendUint16(b, sha1.Size)
	return mac.Sum(b)
}

// CheckIntegrity reports whether b carries a MESSAGE-INTEGRITY made with
// key. Attributes after it (FINGERPRINT) are not covered and are allowed.
func CheckIntegrity(b, key []byte) bool {
	m, ok := Decode(b)
	if !ok {
		return false
	}
	off := headerLen
	for _, a := range m.Attrs {
		if a.Type == AttrMessageIntegrity && len(a.Value) == sha1.Size {
			signed := append([]byte(nil), b[:off]...)
			binary.BigEndian.PutUint16(signed[2:4], uint16(off-headerLen+integrityLen))
			mac := hmac.New(sha1.New, key)
			mac.Write(signed)
			return hmac.Equal(mac.Sum(nil), a.Value)
		}
		off += 4 + len(a.Value) + pad(len(a.Value))
	}
	return false
}

func pad(n int) int { return (4 - n%4) % 4 }
