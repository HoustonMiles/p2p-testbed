package wire

import (
	"bytes"
	"net/netip"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	in := Packet{Type: Ping, Room: 1 << 40, From: 0xfeedface, Seq: 7, Body: U64(123)}
	b := Packet{Type: Ping, Room: in.Room, From: in.From, Seq: 1, Body: in.Body}.Append(nil)
	PutSeq(b, in.Seq)
	out, ok := Parse(b)
	if !ok || out.Type != in.Type || out.Room != in.Room || out.From != in.From ||
		out.Seq != in.Seq || !bytes.Equal(out.Body, in.Body) {
		t.Fatalf("got %+v, want %+v", out, in)
	}
}

func TestIntro(t *testing.T) {
	in := Intro{Self: netip.MustParseAddrPort("203.0.113.10:1000"),
		Peer: netip.MustParseAddrPort("203.0.113.21:65535"), PeerID: 99}
	out, ok := ParseIntro(in.Append(nil))
	if !ok || out.Self != in.Self || out.Peer != in.Peer || out.PeerID != 99 || out.Key != nil || out.Relay.IsValid() {
		t.Fatalf("without extras: got %+v", out)
	}
	relay := netip.MustParseAddrPort("198.18.0.102:50001")
	for _, e := range []Extras{{Key: bytes.Repeat([]byte{7}, KeySize)}, {Relay: relay},
		{Key: bytes.Repeat([]byte{7}, KeySize), Relay: relay}, {Relay: relay, Handoff: true},
		{Key: bytes.Repeat([]byte{7}, KeySize), Relay: relay, Handoff: true}} {
		in.Extras = e
		if out, _ := ParseIntro(in.Append(nil)); !bytes.Equal(out.Key, e.Key) || out.Relay != e.Relay || out.Handoff != e.Handoff {
			t.Fatalf("extras %+v: got %+v", e, out.Extras)
		}
	}
	in.Extras = Extras{Key: make([]byte, 5)}
	if out, ok := ParseIntro(in.Append(nil)); !ok || out.Key != nil {
		t.Fatal("short key accepted")
	}
	if _, ok := ParseIntro(make([]byte, introLen-1)); ok {
		t.Fatal("short body accepted")
	}
}
