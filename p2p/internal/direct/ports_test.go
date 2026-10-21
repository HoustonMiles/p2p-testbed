package direct

import (
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"p2ptestbed/p2p/internal/noise"
)

// received counts the payloads one hopper was handed.
type received struct {
	mu  sync.Mutex
	got map[string]int
}

func (r *received) add(in Inbound) {
	r.mu.Lock()
	r.got[string(in.Payload)]++
	r.mu.Unlock()
}

func (r *received) only(t *testing.T, name, want string, sent int) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.got) != 1 || float64(r.got[want]) < 0.99*float64(sent) {
		t.Errorf("%s got %v, want only %q (%d sent)", name, r.got, want, sent)
	}
}

// sharedHopper hops on a one-port schedule, so every slot of every link
// uses the same port.
func sharedHopper(t *testing.T, sess *noise.Session, peer string, ports *Ports) (*Hopper, *received) {
	t.Helper()
	s := NewSchedule(sess.HopKey(), 200*time.Millisecond)
	s.PortBase, s.PortCount = 31234, 1
	r := &received{got: map[string]int{}}
	h, err := NewHopper(Config{Session: sess, Schedule: s, PeerIP: netip.MustParseAddr(peer), Ports: ports, OnPacket: r.add})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h, r
}

// Peer a has links to b and c whose schedules always pick the same port:
// one socket on a serves both, and every packet still reaches its own link.
func TestPortsShared(t *testing.T) {
	ab, ba := sessions(t)
	ac, ca := sessions(t)
	ports := map[string]*Ports{}
	for _, ip := range []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"} {
		ports[ip] = NewPorts(netip.MustParseAddr(ip))
		t.Cleanup(ports[ip].Close)
	}
	hAB, rAB := sharedHopper(t, ab, "127.0.0.2", ports["127.0.0.1"])
	hAC, rAC := sharedHopper(t, ac, "127.0.0.3", ports["127.0.0.1"])
	hBA, rBA := sharedHopper(t, ba, "127.0.0.1", ports["127.0.0.2"])
	hCA, rCA := sharedHopper(t, ca, "127.0.0.1", ports["127.0.0.3"])

	time.Sleep(50 * time.Millisecond)
	sent := 0
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); sent++ {
		hAB.Send([]byte("a to b"))
		hAC.Send([]byte("a to c"))
		hBA.Send([]byte("b to a"))
		hCA.Send([]byte("c to a"))
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)

	rBA.only(t, "b", "a to b", sent)
	rCA.only(t, "c", "a to c", sent)
	rAB.only(t, "a's link to b", "b to a", sent)
	rAC.only(t, "a's link to c", "c to a", sent)
	for name, h := range map[string]*Hopper{"a-b": hAB, "a-c": hAC} {
		if st := h.Stats(); st.BindFailed > 0 || st.Unrouted > 0 || st.HopsMissed > 0 {
			t.Errorf("%s: %+v", name, st)
		}
	}
}

// A packet naming no session of the peer is dropped before any crypto and
// counted; the real peer's packets are routed to its link.
func TestPortsUnrouted(t *testing.T) {
	ab, ba := sessions(t)
	stranger, _ := sessions(t)
	ports := NewPorts(netip.MustParseAddr("127.0.0.1"))
	defer ports.Close()
	h, r := sharedHopper(t, ab, "127.0.0.9", ports) // a peer nobody runs, so no probes come back
	fromPeer, _ := ba.Seal(nil, TypeData, []byte("for a"))
	if ports.route(fromPeer) != h {
		t.Fatal("the peer's packet was not routed to its link")
	}
	pkt, _ := stranger.Seal(nil, TypeData, []byte("not for you"))
	if ports.route(pkt) != nil {
		t.Fatal("a stranger's packet was routed")
	}
	_, local, _ := h.Current()
	conn, err := ports.acquire(local)
	if err != nil {
		t.Fatal(err)
	}
	defer ports.release(local)
	conn.WriteToUDPAddrPort(pkt, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), local))
	time.Sleep(20 * time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	if ports.Unrouted() != 1 || len(r.got) != 0 {
		t.Fatalf("unrouted %d, delivered %v", ports.Unrouted(), r.got)
	}
}

// Once Close returns, OnPacket is never called again, although the shared
// Ports readers keep running for the peer's other links.
func TestCloseStopsDelivery(t *testing.T) {
	ab, ba := sessions(t)
	pa, pb := NewPorts(netip.MustParseAddr("127.0.0.1")), NewPorts(netip.MustParseAddr("127.0.0.2"))
	defer pa.Close()
	defer pb.Close()
	var closed atomic.Bool
	var late atomic.Int64
	s := NewSchedule(ab.HopKey(), 200*time.Millisecond)
	s.PortBase, s.PortCount = 31234, 1
	a, err := NewHopper(Config{Session: ab, Schedule: s, PeerIP: netip.MustParseAddr("127.0.0.2"), Ports: pa,
		OnPacket: func(Inbound) {
			time.Sleep(2 * time.Millisecond) // a slow handler, so Close catches one in flight
			if closed.Load() {
				late.Add(1)
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := sharedHopper(t, ba, "127.0.0.1", pb)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for end := time.Now().Add(300 * time.Millisecond); time.Now().Before(end); {
			b.Send([]byte("x"))
		}
	}()
	time.Sleep(100 * time.Millisecond)
	a.Close()
	closed.Store(true)
	<-done
	time.Sleep(20 * time.Millisecond)
	if late.Load() > 0 {
		t.Fatalf("%d packets delivered after Close returned", late.Load())
	}
}

// Closing one link gives back its share of a socket; the link still using
// it keeps working.
func TestPortsRelease(t *testing.T) {
	ab, ba := sessions(t)
	ac, _ := sessions(t)
	pa, pb := NewPorts(netip.MustParseAddr("127.0.0.1")), NewPorts(netip.MustParseAddr("127.0.0.2"))
	defer pa.Close()
	defer pb.Close()
	hAB, rAB := sharedHopper(t, ab, "127.0.0.2", pa)
	hAC, _ := sharedHopper(t, ac, "127.0.0.3", pa) // shares a's only port with hAB
	hBA, rBA := sharedHopper(t, ba, "127.0.0.1", pb)
	hAC.Close()
	time.Sleep(50 * time.Millisecond)
	sent := 0
	for end := time.Now().Add(300 * time.Millisecond); time.Now().Before(end); sent++ {
		hAB.Send([]byte("a to b"))
		hBA.Send([]byte("b to a"))
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	rBA.only(t, "b", "a to b", sent)
	rAB.only(t, "a", "b to a", sent)
}
