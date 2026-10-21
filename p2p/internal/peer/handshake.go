package peer

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"p2ptestbed/p2p/internal/noise"
	"p2ptestbed/p2p/internal/wire"
)

// handshake runs Noise with the member over the punched socket: in each
// pair the lower static key initiates; both sides resend their last message
// until done, and answer the member's messages from onHandshake in whatever
// step they are. The prologue binds the handshake to this trial. A failed
// handshake carries its error in the link's row.
func (l *link) handshake() bool {
	if err := l.startHandshake(); err != nil {
		l.fail(fmt.Errorf("noise handshake: %w", err))
		return false
	}
	timeout := l.r.handshakeTimeout()
	l.pump(timeout, l.r.cfg.PunchInterval, l.resendHandshake, l.handshakeOver)
	return l.finishHandshake(timeout)
}

func (r *runner) handshakeTimeout() time.Duration {
	if t := r.cfg.Direct.HandshakeTimeout; t != 0 {
		return t
	}
	return r.cfg.PunchTimeout
}

func (l *link) startHandshake() error {
	hs, err := l.newHandshake()
	if err != nil {
		return err
	}
	l.hs, l.handshakeT = hs, time.Since(l.r.t0)
	l.res.Initiator = ptr(hs.Initiator())
	if hs.Initiator() {
		_, err = hs.Start()
	}
	return err
}

func (l *link) handshakeOver() bool { return l.hs.Done() || l.hs.Failed() != nil }

func (l *link) finishHandshake(timeout time.Duration) bool {
	switch {
	case l.hs.Failed() != nil:
		l.fail(fmt.Errorf("noise handshake: %w", l.hs.Failed()))
	case !l.hs.Done():
		l.fail(fmt.Errorf("noise handshake (%s): not done after %v (right keys and -noise on both peers?)", l.r.cfg.Direct.Noise, timeout))
	default:
		l.noteHandshakeDone(time.Since(l.r.t0)) // already noted, unless it finished outside onHandshake
		l.logf("handshake: %s done in %.1f ms", l.r.cfg.Direct.Noise, *l.res.HandshakeMs)
		return true
	}
	return false
}

func (l *link) newHandshake() (*noise.Handshake, error) {
	peerKey, err := l.expectedPeerKey()
	if err != nil {
		return nil, err
	}
	initiator, err := l.initiates(peerKey)
	if err != nil {
		return nil, err
	}
	return noise.NewHandshake(noise.HandshakeConfig{
		Pattern:   l.r.cfg.Direct.Noise,
		Initiator: initiator,
		Static:    l.r.cfg.Direct.Key,
		PeerKey:   peerKey,
		Prologue:  append([]byte("p2ptestbed direct v2"), wire.U64(l.r.cfg.Room)...),
	})
}

// expectedPeerKey is the key the member must prove it holds: provisioned
// in a file, as the rendezvous announced it, or none (trust on first use).
func (l *link) expectedPeerKey() ([]byte, error) {
	switch d := l.r.cfg.Direct; d.PeerKeyFrom {
	case "file":
		return l.trustedPeerKey()
	case "rendezvous":
		if l.rdvKey == nil {
			return nil, fmt.Errorf("the rendezvous announced no key for the member")
		}
		return l.rdvKey, nil
	case "none":
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown peer key source %q", d.PeerKeyFrom)
	}
}

// trustedPeerKey is the trusted key the rendezvous says the member has. The
// handshake then proves the member holds it, so a lying rendezvous can only
// make the handshake fail. With one trusted key, the rendezvous is not
// needed at all.
func (l *link) trustedPeerKey() ([]byte, error) {
	trusted := l.r.cfg.Direct.TrustedKeys
	if l.rdvKey == nil && len(trusted) == 1 {
		return trusted[0], nil
	}
	for _, k := range trusted {
		if bytes.Equal(k, l.rdvKey) {
			return k, nil
		}
	}
	return nil, fmt.Errorf("the member's key %x is not one of the %d trusted keys", l.rdvKey, len(trusted))
}

// initiates reports whether we start the handshake. Ordering by static key
// lets any two members of a mesh agree without being told roles. Under
// -peer-key none the rendezvous's key only sets the order; the handshake
// itself is what authenticates the member.
func (l *link) initiates(peerKey []byte) (bool, error) {
	if peerKey == nil {
		peerKey = l.rdvKey
	}
	order := bytes.Compare(l.r.cfg.Direct.Key.Public, peerKey)
	switch {
	case peerKey == nil:
		return false, errors.New("cannot tell who starts the handshake: no key for the member from a file or the rendezvous")
	case order == 0:
		return false, errors.New("the member has our static key (each peer needs its own: punch -genkey)")
	}
	return order < 0, nil
}

func (l *link) resendHandshake() {
	if last := l.hs.Last(); last != nil && !l.hs.Done() {
		l.sendPlain(l.remote, wire.Handshake, 0, last)
	}
}

// onHandshake answers the member's handshake messages. Before our own
// handshake starts there is nothing to answer with; the initiator retries.
func (l *link) onHandshake(m inbound) {
	if l.hs == nil || m.hop {
		return
	}
	reply, err := l.hs.Handle(m.pkt.Body)
	if err != nil {
		l.logf("handshake: dropped a bad message from %v: %v", m.from, err)
		return
	}
	if reply != nil {
		l.sendPlain(m.from, wire.Handshake, 0, reply)
	}
	l.noteHandshakeDone(m.at)
}

func (l *link) noteHandshakeDone(at time.Duration) {
	if l.hs.Done() && l.res.HandshakeMs == nil {
		l.res.HandshakeMs = ptr(ms(at - l.handshakeT))
	}
}
