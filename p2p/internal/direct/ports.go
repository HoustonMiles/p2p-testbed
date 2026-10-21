package direct

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"

	"p2ptestbed/p2p/internal/noise"
)

// Ports is one peer's hop sockets, shared by all its hoppers. Two links
// whose schedules pick the same local port in the same slot use the same
// socket, and each arriving packet goes to the hopper whose session its
// receiver index names. Safe for concurrent use.
type Ports struct {
	localIP netip.Addr // zero binds all interfaces

	mu       sync.Mutex
	socks    map[uint16]*portSocket
	hoppers  map[uint32]*Hopper // by the session's local index
	unrouted atomic.Uint64
	wg       sync.WaitGroup
}

type portSocket struct {
	conn *net.UDPConn
	refs int // hoppers using it
}

var ErrIndexTaken = errors.New("direct: another session on this peer has the same receiver index")

func NewPorts(localIP netip.Addr) *Ports {
	return &Ports{localIP: localIP, socks: map[uint16]*portSocket{}, hoppers: map[uint32]*Hopper{}}
}

// Unrouted counts packets naming no session of this peer: stray or forged
// traffic, dropped before any crypto.
func (p *Ports) Unrouted() uint64 { return p.unrouted.Load() }

// Close closes every socket and waits for the readers to stop.
func (p *Ports) Close() {
	p.mu.Lock()
	for port, s := range p.socks {
		s.conn.Close()
		delete(p.socks, port)
	}
	p.mu.Unlock()
	p.wg.Wait()
}

func (p *Ports) add(h *Hopper) error {
	idx := h.cfg.Session.LocalIndex()
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, taken := p.hoppers[idx]; taken {
		return ErrIndexTaken
	}
	p.hoppers[idx] = h
	return nil
}

func (p *Ports) remove(h *Hopper) {
	idx := h.cfg.Session.LocalIndex()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hoppers[idx] == h {
		delete(p.hoppers, idx)
	}
}

// acquire returns the socket for port, binding it on first use.
func (p *Ports) acquire(port uint16) (*net.UDPConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s := p.socks[port]; s != nil {
		s.refs++
		return s.conn, nil
	}
	var ip net.IP
	if p.localIP.IsValid() {
		ip = p.localIP.AsSlice()
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: int(port)})
	if err != nil {
		return nil, err
	}
	conn.SetReadBuffer(4 << 20)
	conn.SetWriteBuffer(4 << 20)
	p.socks[port] = &portSocket{conn: conn, refs: 1}
	p.wg.Add(1)
	go p.readLoop(port, conn)
	return conn, nil
}

// release closes port's socket once no hopper uses it.
func (p *Ports) release(port uint16) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.socks[port]
	if s == nil {
		return
	}
	if s.refs--; s.refs == 0 {
		s.conn.Close() // its reader sees the error and exits
		delete(p.socks, port)
	}
}

func (p *Ports) readLoop(port uint16, conn *net.UDPConn) {
	defer p.wg.Done()
	buf := make([]byte, 65536)
	plain := make([]byte, 0, 65536)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		h := p.route(buf[:n])
		if h == nil {
			p.unrouted.Add(1)
			continue
		}
		h.receive(port, netip.AddrPortFrom(from.Addr().Unmap(), from.Port()), buf[:n], plain[:0])
	}
}

func (p *Ports) route(pkt []byte) *Hopper {
	idx, ok := noise.ReceiverIndex(pkt)
	if !ok {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hoppers[idx]
}
