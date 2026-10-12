package peer

import (
	"net"
	"sync"
	"testing"
	"time"

	"p2ptestbed/baseline/internal/rendezvous"
	"p2ptestbed/baseline/internal/stun"
)

// End to end on loopback, no NAT: a fake STUN server, the real rendezvous
// server and both peers. Checks every step completes and reports sensibly.
func TestLoopback(t *testing.T) {
	stunConn := listen(t)
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := stunConn.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			if stun.IsMessage(buf[:n]) {
				stunConn.WriteToUDPAddrPort(stun.Response(stun.TxID(buf[8:20]), from), from)
			}
		}
	}()
	rdvConn := listen(t)
	go (&rendezvous.Server{Conn: rdvConn, TTL: 5 * time.Second}).Serve()

	results := map[byte]*Result{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, role := range []byte{'a', 'b'} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := Run(Config{
				Role: role, Session: 42, Local: "127.0.0.1:0",
				STUN: stunConn.LocalAddr().String(), Rendezvous: rdvConn.LocalAddr().String(),
				STUNTimeout: time.Second, RendezvousTimeout: 5 * time.Second,
				PunchTimeout: 5 * time.Second, PunchInterval: 20 * time.Millisecond,
				StepTimeout: 5 * time.Second,
				Pings:       20, PingInterval: 5 * time.Millisecond, PingGrace: 200 * time.Millisecond,
				RateMbit: 5, Duration: 300 * time.Millisecond, Size: 1000, Drain: 200 * time.Millisecond,
				Linger: 100 * time.Millisecond, Logf: t.Logf,
			})
			if err != nil {
				t.Errorf("%c: %v", role, err)
			}
			mu.Lock()
			results[role] = res
			mu.Unlock()
		}()
	}
	wg.Wait()

	for role, r := range results {
		if !r.Punched || r.PunchMs == nil {
			t.Errorf("%c: not punched", role)
		}
		if r.STUNAddr == nil || *r.STUNAddr != r.Local || r.MappingVaries == nil || *r.MappingVaries {
			t.Errorf("%c: stun %v, local %v", role, r.STUNAddr, r.Local)
		}
		if r.PeerAddrChanged == nil || *r.PeerAddrChanged {
			t.Errorf("%c: peer address changed without a NAT", role)
		}
		if r.PingsRecv != 20 || r.RTTP50 == nil {
			t.Errorf("%c: %d/20 pings", role, r.PingsRecv)
		}
		if r.TxPackets == nil || *r.TxPackets == 0 {
			t.Errorf("%c: sent nothing", role)
		}
		if r.RxPackets == nil || r.RxPeerSent == nil || *r.RxPackets != *r.RxPeerSent || *r.RxLoss != 0 {
			t.Errorf("%c: received %v of %v", role, r.RxPackets, r.RxPeerSent)
		}
		if r.RxMbit == nil || *r.RxMbit < 3 || *r.RxMbit > 7 {
			t.Errorf("%c: rx rate %v, want about 5 Mbit/s", role, r.RxMbit)
		}
	}
	if *results['a'].TxPackets != *results['b'].RxPeerSent {
		t.Error("b did not learn how many packets a sent")
	}
}

func listen(t *testing.T) *net.UDPConn {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
