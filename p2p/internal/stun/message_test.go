package stun

import (
	"bytes"
	"encoding/hex"
	"net/netip"
	"strings"
	"testing"
)

// The sample request of RFC 5769 section 2.1, signed with the short-term
// password "VOkJxbRl1RmTxUk/WvJxBt" (and followed by a FINGERPRINT).
var rfc5769Request = strings.Join([]string{
	"000100582112a442b7e7a701bc34d686fa87dfae",
	"802200105354554e207465737420636c69656e74",
	"002400046e0001ff",
	"80290008932ff9b151263b36",
	"000600096576746a3a68367659202020",
	"000800149aeaa70cbfd8cb56781ef2b5b2d3f249c1b571a2",
	"80280004e57a3bcf",
}, "")

func TestIntegrityRFC5769(t *testing.T) {
	b, _ := hex.DecodeString(rfc5769Request)
	if !CheckIntegrity(b, []byte("VOkJxbRl1RmTxUk/WvJxBt")) {
		t.Fatal("the RFC's own sample request does not check")
	}
	if CheckIntegrity(b, []byte("wrong password")) {
		t.Fatal("checked with the wrong key")
	}
	m, ok := Decode(b)
	if user, _ := m.Get(0x0006); !ok || string(user) != "evtj:h6vY" || len(m.Attrs) != 6 {
		t.Fatalf("decoded %v %v", ok, m.Attrs)
	}
}

func TestEncodeSignCheck(t *testing.T) {
	peer := netip.MustParseAddrPort("198.18.0.102:50123")
	m := Message{Type: 0x0009, ID: NewTxID(), Attrs: []Attr{{0x0012, XORAddress(peer)}, {0x0006, []byte("odd")}}}
	b := AppendIntegrity(m.Encode(), []byte("key"))
	if !CheckIntegrity(b, []byte("key")) {
		t.Fatal("own signature does not check")
	}
	got, ok := Decode(b)
	if v, _ := got.Get(0x0012); !ok || got.ID != m.ID || !bytes.Equal(v, XORAddress(peer)) {
		t.Fatalf("round trip: %v %v", ok, got)
	}
	if a, ok := ParseXORAddress(XORAddress(peer)); !ok || a != peer {
		t.Fatalf("xor address %v", a)
	}
	b[len(b)-30] ^= 1 // inside the signed part
	if CheckIntegrity(b, []byte("key")) {
		t.Fatal("tampered message checks")
	}
}
