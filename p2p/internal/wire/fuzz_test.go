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
	f.Add(Packet{Type: Ping, Room: 1, From: 9, Seq: 2, Body: U64(3)}.Append(nil))
	f.Add([]byte(Magic))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		p, ok := Parse(b)
		if ok && !bytes.Equal(p.Append(nil), b) {
			t.Fatalf("accepted %x but re-encodes differently", b)
		}
	})
}

// A PeerInfo body that is accepted re-encodes to its prefix (a tail of the
// wrong length is dropped).
func FuzzParseIntro(f *testing.F) {
	in := Intro{Self: netip.MustParseAddrPort("203.0.113.10:1"),
		Peer: netip.MustParseAddrPort("203.0.113.20:65535"), PeerID: 5}
	f.Add(in.Append(nil))
	in.Key = bytes.Repeat([]byte{1}, KeySize)
	f.Add(in.Append(nil))
	in.Relay = netip.MustParseAddrPort("198.18.0.102:50001")
	f.Add(in.Append(nil))
	in.Handoff = true
	f.Add(in.Append(nil))
	f.Add([]byte{1, 2, 3})
	f.Fuzz(func(t *testing.T, b []byte) {
		got, ok := ParseIntro(b)
		if ok && !bytes.HasPrefix(b, got.Append(nil)) {
			t.Fatalf("accepted %x but re-encodes differently", b)
		}
	})
}
