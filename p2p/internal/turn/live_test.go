package turn

import (
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"p2ptestbed/p2p/internal/stun"
)

// TestLiveRelayToRelay runs against a real TURN server, e.g. the testbed's
// relay from the host (skipped without TURN_SERVER):
//
//	TURN_SERVER=198.18.0.102:3478 go test -run Live -v ./internal/turn
//
// Two clients each allocate a relayed address and send to each other's,
// as two peers in relayed mode do: neither ever sends to the other's own
// address.
func TestLiveRelayToRelay(t *testing.T) {
	addr := os.Getenv("TURN_SERVER")
	if addr == "" {
		t.Skip("set TURN_SERVER=host:port to run against a real relay")
	}
	server := netip.MustParseAddrPort(addr)
	a, b := liveClient(t, server), liveClient(t, server)
	a.do(t, func(id stun.TxID) []byte { return a.c.ChannelBind(id, MinChannel, b.relayed) })
	b.do(t, func(id stun.TxID) []byte { return b.c.ChannelBind(id, MinChannel+7, a.relayed) })

	a.conn.WriteToUDPAddrPort(ChannelData(MinChannel, []byte("hello through the relay")), server)
	ch, data := b.receive(t)
	if ch != MinChannel+7 || string(data) != "hello through the relay" {
		t.Fatalf("b got channel %#x %q", ch, data)
	}
	t.Logf("a %v -> relay -> b %v", a.relayed, b.relayed)
	for _, x := range []*live{a, b} {
		x.do(t, func(id stun.TxID) []byte { return x.c.Refresh(id, 0) }) // release the allocation
	}
}

type live struct {
	conn    *net.UDPConn
	server  netip.AddrPort
	c       *Client
	relayed netip.AddrPort
}

func liveClient(t *testing.T, server netip.AddrPort) *live {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	x := &live{conn: conn, server: server, c: &Client{Username: "testbed", Password: "testbed"}}
	r := x.do(t, x.c.Allocate)
	if !r.Relayed.IsValid() {
		t.Fatalf("allocation without a relayed address: %+v", r)
	}
	x.relayed = r.Relayed
	return x
}

// do sends a request until it succeeds, retrying once credentials are learned.
func (x *live) do(t *testing.T, build func(stun.TxID) []byte) Response {
	t.Helper()
	for range 3 {
		id := stun.NewTxID()
		x.conn.WriteToUDPAddrPort(build(id), x.server)
		r := x.reply(t, id)
		if r.OK {
			return r
		}
		if !r.Retry {
			t.Fatalf("request refused: %d %s", r.Code, r.Reason)
		}
	}
	t.Fatal("still refused after retrying")
	return Response{}
}

func (x *live) reply(t *testing.T, id stun.TxID) Response {
	t.Helper()
	buf := make([]byte, 1500)
	x.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		n, _, err := x.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			t.Fatalf("no reply from %v: %v", x.server, err)
		}
		if r, ok := x.c.Read(buf[:n]); ok && r.ID == id {
			return r
		}
	}
}

func (x *live) receive(t *testing.T) (uint16, []byte) {
	t.Helper()
	buf := make([]byte, 1500)
	x.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		n, _, err := x.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			t.Fatalf("nothing relayed: %v", err)
		}
		if ch, data, ok := ParseChannelData(buf[:n]); ok {
			return ch, data
		}
	}
}
