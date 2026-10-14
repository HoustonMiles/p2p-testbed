package stun

import (
	"net/netip"
	"testing"
)

// Parse reads replies from the network; it must never panic, and it must
// only accept a reply to our own transaction.
//
//	go test -fuzz=FuzzParse -fuzztime=30s ./internal/stun
func FuzzParse(f *testing.F) {
	id := TxID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	f.Add(Response(id, netip.MustParseAddrPort("203.0.113.10:51763")))
	f.Add(Request(id))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		if _, ok := Parse(b, id); ok && TxID(b[8:20]) != id {
			t.Fatalf("accepted a reply to another transaction: %x", b)
		}
	})
}
