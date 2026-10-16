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
	if !ok || out.Self != in.Self || out.Peer != in.Peer || out.PeerID != 99 || out.PeerKey != nil {
		t.Fatalf("without a key: got %+v", out)
	}
	in.PeerKey = bytes.Repeat([]byte{7}, KeySize)
	if out, _ := ParseIntro(in.Append(nil)); !bytes.Equal(out.PeerKey, in.PeerKey) {
		t.Fatalf("key: got %x", out.PeerKey)
	}
	in.PeerKey = in.PeerKey[:5]
	if out, ok := ParseIntro(in.Append(nil)); !ok || out.PeerKey != nil {
		t.Fatal("short key accepted")
	}
	if _, ok := ParseIntro(make([]byte, introLen-1)); ok {
		t.Fatal("short body accepted")
	}
}
