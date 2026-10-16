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
