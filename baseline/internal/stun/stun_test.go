package stun

import (
	"net/netip"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	id := NewTxID()
	want := netip.MustParseAddrPort("203.0.113.10:51763")
	resp := Response(id, want)
	if !IsMessage(resp) || !IsMessage(Request(id)) {
		t.Fatal("not recognised as STUN")
	}
	got, ok := Parse(resp, id)
	if !ok || got != want {
		t.Fatalf("got %v %v, want %v", got, ok, want)
	}
	if _, ok := Parse(resp, NewTxID()); ok {
		t.Fatal("accepted a response for another transaction")
	}
}
