package wire

import (
	"bytes"
	"net/netip"
	"testing"
)

// Parse sees every datagram that reaches the punched socket, so it must
// never panic, and whatever it accepts must encode back to the same bytes.
//
//	go test -fuzz=FuzzParse -fuzztime=30s ./internal/wire
func FuzzParse(f *testing.F) {
	f.Add(Packet{Type: Ping, Role: 'a', Session: 1, Seq: 2, Body: U64(3)}.Append(nil))
	f.Add([]byte(Magic))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		p, ok := Parse(b)
		if ok && !bytes.Equal(p.Append(nil), b) {
			t.Fatalf("accepted %x but re-encodes differently", b)
		}
	})
}

func FuzzParseAddrs(f *testing.F) {
	f.Add(Addrs(netip.MustParseAddrPort("203.0.113.10:1"), netip.MustParseAddrPort("203.0.113.20:65535")))
	f.Add([]byte{1, 2, 3})
	f.Fuzz(func(t *testing.T, b []byte) {
		self, peer, ok := ParseAddrs(b)
		if ok && !bytes.Equal(Addrs(self, peer), b[:12]) {
			t.Fatalf("accepted %x but re-encodes differently", b)
		}
	})
}
