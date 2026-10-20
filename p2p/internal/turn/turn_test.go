package turn

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"p2ptestbed/p2p/internal/stun"
)

var relayed = netip.MustParseAddrPort("198.18.0.102:50001")

// server answers like coturn: the realm and nonce in errors, and success
// responses signed with the long-term key.
type server struct{ realm, nonce, password string }

func (s server) key(user string) []byte {
	sum := md5.Sum([]byte(user + ":" + s.realm + ":" + s.password))
	return sum[:]
}

func (s server) fail(req []byte, code int) []byte {
	m, _ := stun.Decode(req)
	val := []byte{0, 0, byte(code / 100), byte(code % 100)}
	return stun.Message{Type: messageType(methodOf(m.Type), classError), ID: m.ID, Attrs: []stun.Attr{
		{Type: attrErrorCode, Value: append(val, "Unauthorized"...)},
		{Type: attrRealm, Value: []byte(s.realm)}, {Type: attrNonce, Value: []byte(s.nonce)},
	}}.Encode()
}

func (s server) allocated(req []byte, user string) []byte {
	m, _ := stun.Decode(req)
	return stun.AppendIntegrity(stun.Message{Type: messageType(methodAllocate, classSuccess), ID: m.ID, Attrs: []stun.Attr{
		{Type: attrXORRelayedAddress, Value: stun.XORAddress(relayed)},
		{Type: attrXORMappedAddress, Value: stun.XORAddress(netip.MustParseAddrPort("198.18.1.10:40000"))},
		{Type: attrLifetime, Value: binary.BigEndian.AppendUint32(nil, 600)},
	}}.Encode(), s.key(user))
}

func TestAllocateWithLongTermCredentials(t *testing.T) {
	s := server{realm: "p2p-testbed", nonce: "n1", password: "testbed"}
	c := &Client{Username: "testbed", Password: "testbed"}
	first := c.Allocate(stun.NewTxID())
	if m, _ := stun.Decode(first); len(m.Attrs) != 1 {
		t.Fatalf("first request carries %d attributes, want only REQUESTED-TRANSPORT", len(m.Attrs))
	}
	if r, ok := c.Read(s.fail(first, 401)); !ok || !r.Retry || r.Code != 401 {
		t.Fatalf("401: %+v %v, want a retry", r, ok)
	}
	second := c.Allocate(stun.NewTxID())
	if !stun.CheckIntegrity(second, s.key("testbed")) {
		t.Fatal("the retry is not signed with MD5(user:realm:password)")
	}
	r, ok := c.Read(s.allocated(second, "testbed"))
	if !ok || !r.OK || r.Method != methodAllocate || r.Relayed != relayed || r.Lifetime != 600*time.Second {
		t.Fatalf("success: %+v %v", r, ok)
	}
}

func TestRejectsForgedAndWrongCredentials(t *testing.T) {
	s := server{realm: "p2p-testbed", nonce: "n1", password: "testbed"}
	c := &Client{Username: "testbed", Password: "wrong"}
	c.Read(s.fail(c.Allocate(stun.NewTxID()), 401))
	req := c.Allocate(stun.NewTxID())
	if _, ok := c.Read(s.allocated(req, "someone else")); ok {
		t.Fatal("accepted a success response not signed with our key")
	}
	if r, ok := c.Read(s.fail(req, 401)); !ok || r.Retry {
		t.Fatalf("a 401 to signed credentials must not retry (they are wrong): %+v", r)
	}
}

func TestStaleNonceRetriesWithTheNewOne(t *testing.T) {
	s := server{realm: "p2p-testbed", nonce: "n1", password: "testbed"}
	c := &Client{Username: "testbed", Password: "testbed"}
	c.Read(s.fail(c.Allocate(stun.NewTxID()), 401))
	s.nonce = "n2"
	if r, _ := c.Read(s.fail(c.Refresh(stun.NewTxID(), time.Minute), 438)); !r.Retry {
		t.Fatal("438 did not retry")
	}
	m, _ := stun.Decode(c.Refresh(stun.NewTxID(), time.Minute))
	if nonce, _ := m.Get(attrNonce); string(nonce) != "n2" {
		t.Fatalf("nonce %q, want the new one", nonce)
	}
}

func TestChannelBindAndData(t *testing.T) {
	c := &Client{}
	m, _ := stun.Decode(c.ChannelBind(stun.NewTxID(), MinChannel, relayed))
	num, _ := m.Get(attrChannelNumber)
	peer, _ := m.Get(attrXORPeerAddress)
	if methodOf(m.Type) != methodChannelBind || binary.BigEndian.Uint16(num) != MinChannel || !bytes.Equal(peer, stun.XORAddress(relayed)) {
		t.Fatalf("channel bind %+v", m)
	}
	b := ChannelData(MinChannel+1, []byte("hello"))
	if stun.IsMessage(b) || !IsChannelData(b) {
		t.Fatal("ChannelData is not told apart from STUN")
	}
	if ch, data, ok := ParseChannelData(b); !ok || ch != MinChannel+1 || string(data) != "hello" {
		t.Fatalf("parsed %x %q %v", ch, data, ok)
	}
	if _, _, ok := ParseChannelData(b[:6]); ok {
		t.Fatal("accepted a truncated ChannelData")
	}
	if IsChannelData(stun.Request(stun.NewTxID())) {
		t.Fatal("took STUN for ChannelData")
	}
}

func TestMethodBits(t *testing.T) {
	for _, m := range []uint16{methodAllocate, methodRefresh, methodCreatePermission, methodChannelBind, 0xFFF} {
		for _, class := range []uint16{classRequest, classSuccess, classError} {
			if got := methodOf(messageType(m, class)); got != m {
				t.Errorf("method %#x class %#x -> %#x", m, class, got)
			}
		}
	}
	if messageType(methodAllocate, classSuccess) != 0x0103 || messageType(methodChannelBind, classError) != 0x0119 {
		t.Fatal("wrong wire types")
	}
}

// Read takes replies from the network: it must never panic.
//
//	go test -fuzz=FuzzRead -fuzztime=30s ./internal/turn
func FuzzRead(f *testing.F) {
	s := server{realm: "r", nonce: "n", password: "p"}
	c := &Client{Username: "u", Password: "p"}
	req := c.Allocate(stun.TxID{})
	f.Add(s.fail(req, 401))
	f.Add(s.allocated(req, "u"))
	f.Add(ChannelData(MinChannel, []byte("x")))
	f.Fuzz(func(t *testing.T, b []byte) {
		c := &Client{Username: "u", Password: "p"}
		c.Read(b)
		ParseChannelData(b)
	})
}
