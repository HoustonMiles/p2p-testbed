// Package turn is the client side of a TURN relay (RFC 8656) that a peer
// needs: allocate a relayed address with long-term credentials, permit the
// members' addresses, bind a channel per member, refresh, and frame data
// as ChannelData. Like package stun, it never touches a socket: it builds
// requests and reads responses, and the peer carries them.
//
// A request is answered 401 until it carries the server's realm and nonce;
// Read learns them and says to send the request again (Response.Retry),
// as it does when the nonce goes stale (438). A 401 to a signed request
// means the credentials are wrong; a 401 to an unsigned one, even one that
// arrives after we learned the key (a resent request's late reply), never
// does.
package turn

import (
	"crypto/md5"
	"encoding/binary"
	"net/netip"
	"time"

	"p2ptestbed/p2p/internal/stun"
)

// Methods (RFC 8656 section 18) and message classes (RFC 8489 section 5).
const (
	methodAllocate         = 0x003
	methodRefresh          = 0x004
	methodCreatePermission = 0x008
	methodChannelBind      = 0x009

	classRequest = 0x0000
	classSuccess = 0x0100
	classError   = 0x0110
)

// Attributes.
const (
	attrChannelNumber      = 0x000C
	attrLifetime           = 0x000D
	attrUsername           = 0x0006
	attrErrorCode          = 0x0009
	attrXORPeerAddress     = 0x0012
	attrRealm              = 0x0014
	attrNonce              = 0x0015
	attrXORRelayedAddress  = 0x0016
	attrRequestedTransport = 0x0019
	attrXORMappedAddress   = 0x0020
)

// Channel numbers a client may bind (RFC 8656 section 12).
const (
	MinChannel = 0x4000
	MaxChannel = 0x4FFF
)

const protocolUDP = 17

// Client holds one allocation's credentials and what the server told it.
type Client struct {
	Username, Password string

	realm, nonce []byte
	key          []byte                 // MD5(username:realm:password), once the realm is known
	unsigned     map[stun.TxID]struct{} // requests sent before we had the key, until answered
}

// Response is what Read made of a reply to one of our requests.
type Response struct {
	Method uint16
	ID     stun.TxID
	OK     bool   // a success response
	Retry  bool   // the server asked for (fresh) credentials, now learned: send the request again, with a new ID
	Code   int    // error code, if not OK
	Reason string // its reason phrase

	Relayed  netip.AddrPort // Allocate: our relayed address
	Mapped   netip.AddrPort // Allocate: our address as the server sees it
	Lifetime time.Duration  // Allocate, Refresh: how long the allocation lasts unless refreshed
}

// Allocate asks for a relayed UDP address.
func (c *Client) Allocate(id stun.TxID) []byte {
	return c.request(methodAllocate, id, stun.Attr{Type: attrRequestedTransport, Value: []byte{protocolUDP, 0, 0, 0}})
}

// CreatePermission lets the peer's IP address send to our relayed address
// (a permission covers every port of that IP).
func (c *Client) CreatePermission(id stun.TxID, peer netip.AddrPort) []byte {
	return c.request(methodCreatePermission, id, stun.Attr{Type: attrXORPeerAddress, Value: stun.XORAddress(peer)})
}

// ChannelBind binds a channel number to the peer's address, so data to and
// from it travels as ChannelData; it also creates the permission.
func (c *Client) ChannelBind(id stun.TxID, channel uint16, peer netip.AddrPort) []byte {
	num := binary.BigEndian.AppendUint16(nil, channel)
	return c.request(methodChannelBind, id,
		stun.Attr{Type: attrChannelNumber, Value: append(num, 0, 0)},
		stun.Attr{Type: attrXORPeerAddress, Value: stun.XORAddress(peer)})
}

// Refresh keeps the allocation for lifetime more; 0 releases it.
func (c *Client) Refresh(id stun.TxID, lifetime time.Duration) []byte {
	secs := binary.BigEndian.AppendUint32(nil, uint32(lifetime/time.Second))
	return c.request(methodRefresh, id, stun.Attr{Type: attrLifetime, Value: secs})
}

func (c *Client) request(method uint16, id stun.TxID, attrs ...stun.Attr) []byte {
	m := stun.Message{Type: messageType(method, classRequest), ID: id, Attrs: attrs}
	if c.key == nil {
		if c.unsigned == nil {
			c.unsigned = map[stun.TxID]struct{}{}
		}
		c.unsigned[id] = struct{}{}
		return m.Encode()
	}
	m.Attrs = append(m.Attrs,
		stun.Attr{Type: attrUsername, Value: []byte(c.Username)},
		stun.Attr{Type: attrRealm, Value: c.realm},
		stun.Attr{Type: attrNonce, Value: c.nonce})
	return stun.AppendIntegrity(m.Encode(), c.key)
}

// Read interprets a server's reply. ok is false for anything that is not a
// response, and for a success response whose MESSAGE-INTEGRITY does not
// check: once we have a key, only the server that knows it can answer.
func (c *Client) Read(b []byte) (r Response, ok bool) {
	m, ok := stun.Decode(b)
	if !ok {
		return r, false
	}
	r.Method, r.ID = methodOf(m.Type), m.ID
	_, unsigned := c.unsigned[m.ID]
	delete(c.unsigned, m.ID)
	switch m.Type & classError {
	case classSuccess:
		if c.key != nil && !stun.CheckIntegrity(b, c.key) {
			return r, false
		}
		r.OK = true
		readSuccess(m, &r)
	case classError:
		c.readError(m, &r, unsigned)
	default:
		return r, false
	}
	return r, true
}

func readSuccess(m stun.Message, r *Response) {
	if v, ok := m.Get(attrXORRelayedAddress); ok {
		r.Relayed, _ = stun.ParseXORAddress(v)
	}
	if v, ok := m.Get(attrXORMappedAddress); ok {
		r.Mapped, _ = stun.ParseXORAddress(v)
	}
	if v, ok := m.Get(attrLifetime); ok && len(v) == 4 {
		r.Lifetime = time.Duration(binary.BigEndian.Uint32(v)) * time.Second
	}
}

// readError reads the error code. 401 to an unsigned request, or 438
// (stale nonce), carries the realm and nonce to use: learn them and retry.
// A 401 to a signed request means the credentials are wrong; retrying
// would only loop.
func (c *Client) readError(m stun.Message, r *Response, unsigned bool) {
	if v, ok := m.Get(attrErrorCode); ok && len(v) >= 4 {
		r.Code, r.Reason = int(v[2]&0x07)*100+int(v[3]), string(v[4:])
	}
	realm, hasRealm := m.Get(attrRealm)
	nonce, hasNonce := m.Get(attrNonce)
	if !hasNonce || (r.Code != 401 && r.Code != 438) || (r.Code == 401 && !unsigned) {
		return
	}
	if hasRealm {
		c.realm = append([]byte(nil), realm...)
	}
	if c.realm == nil {
		return
	}
	c.nonce = append([]byte(nil), nonce...)
	sum := md5.Sum([]byte(c.Username + ":" + string(c.realm) + ":" + c.Password))
	c.key, r.Retry = sum[:], true
}

// messageType interleaves a method's bits with the class bits (RFC 8489
// section 5): M11..M7, C1, M6..M4, C0, M3..M0.
func messageType(method, class uint16) uint16 {
	return method&0x000F | (method&0x0070)<<1 | (method&0x0F80)<<2 | class
}

func methodOf(t uint16) uint16 { return t&0x000F | (t&0x00E0)>>1 | (t&0x3E00)>>2 }

// ChannelData frames data for a bound channel: channel number, length,
// data. Over UDP no padding is needed (RFC 8656 section 12.5).
func ChannelData(channel uint16, data []byte) []byte {
	b := make([]byte, 4, 4+len(data))
	binary.BigEndian.PutUint16(b[0:2], channel)
	binary.BigEndian.PutUint16(b[2:4], uint16(len(data)))
	return append(b, data...)
}

// IsChannelData tells ChannelData from STUN on a shared socket: its first
// two bits are 01, STUN's are 00.
func IsChannelData(b []byte) bool { return len(b) >= 4 && b[0]&0xC0 == 0x40 }

// ParseChannelData returns the channel and the data of a ChannelData message.
func ParseChannelData(b []byte) (channel uint16, data []byte, ok bool) {
	if !IsChannelData(b) {
		return 0, nil, false
	}
	n := int(binary.BigEndian.Uint16(b[2:4]))
	if 4+n > len(b) {
		return 0, nil, false
	}
	return binary.BigEndian.Uint16(b[0:2]), b[4 : 4+n], true
}
