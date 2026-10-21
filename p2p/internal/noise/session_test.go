package noise

import (
	"bytes"
	"testing"
)

// The packet types belong to the caller (package direct); any bytes do here.
const typeData, typeProbe byte = 1, 2

func TestTransport(t *testing.T) {
	a, b := handshake(t)
	if !bytes.Equal(a.HopKey(), b.HopKey()) {
		t.Fatal("hop keys differ")
	}
	for _, dir := range []struct{ from, to *Session }{{a, b}, {b, a}} {
		pkt, _ := dir.from.Seal(nil, typeData, []byte("hello"))
		typ, got, err := dir.to.Open(nil, pkt)
		if err != nil || typ != typeData || string(got) != "hello" {
			t.Fatalf("open: %v %d %q", err, typ, got)
		}
		if _, _, err := dir.to.Open(nil, pkt); err != ErrReplay {
			t.Fatalf("replay accepted: %v", err)
		}
	}
}

func TestTamperAndReorder(t *testing.T) {
	a, b := handshake(t)
	var pkts [][]byte
	for i := 0; i < 100; i++ {
		p, _ := a.Seal(nil, typeData, []byte{byte(i)})
		pkts = append(pkts, p)
	}
	bad := append([]byte(nil), pkts[0]...)
	bad[len(bad)-1] ^= 1
	if _, _, err := b.Open(nil, bad); err != ErrAuth {
		t.Fatalf("tampered packet: %v", err)
	}
	bad = append([]byte(nil), pkts[0]...)
	bad[0] = typeProbe // the type is authenticated too
	if _, _, err := b.Open(nil, bad); err != ErrAuth {
		t.Fatalf("retyped packet: %v", err)
	}
	// Reverse order, inside the window: all accepted exactly once.
	for i := len(pkts) - 1; i >= 0; i-- {
		if _, _, err := b.Open(nil, pkts[i]); err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
	}
}

// A packet names its receiver's index, the two sides have different ones,
// and the index is authenticated, so it cannot be rewritten to reach
// another session.
func TestReceiverIndex(t *testing.T) {
	a, b := handshake(t)
	if a.LocalIndex() == b.LocalIndex() {
		t.Fatal("both sides have the same index")
	}
	pkt, _ := a.Seal(nil, typeData, []byte("hi"))
	if idx, ok := ReceiverIndex(pkt); !ok || idx != b.LocalIndex() {
		t.Fatalf("index %x, want the receiver's %x", idx, b.LocalIndex())
	}
	pkt[indexOffset] ^= 1
	if _, _, err := b.Open(nil, pkt); err != ErrAuth {
		t.Fatalf("rewritten index: %v", err)
	}
	if _, ok := ReceiverIndex(pkt[:Overhead-1]); ok {
		t.Fatal("index read from a short packet")
	}
}
