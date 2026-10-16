package peer

import (
	"bytes"
	"errors"
	"fmt"
	"p2ptestbed/p2p/internal/noise"
	"p2ptestbed/p2p/internal/wire"
	"time"
)

// handshake runs Noise over the punched socket: the peer with the lower
// static key initiates; both sides resend their last message until done,
// and answer the peer's messages from onHandshake in whatever step they
// are. The prologue binds the handshake to this trial.
func (r *runner) handshake() error {
	hs, err := r.newHandshake()
	if err != nil {
		return err
	}
	r.hs = hs
	r.res.Initiator = ptr(hs.Initiator())
	if hs.Initiator() {
		if _, err := hs.Start(); err != nil {
			return err
		}
	}
	timeout := r.cfg.Direct.HandshakeTimeout
	if timeout == 0 {
		timeout = r.cfg.PunchTimeout
	}
	began := time.Since(r.t0)
	r.pump(timeout, r.cfg.PunchInterval, r.resendHandshake, func() bool { return hs.Done() || hs.Failed() != nil })
	if err := hs.Failed(); err != nil {
		return fmt.Errorf("noise handshake: %w", err)
	}
	if !hs.Done() {
		return fmt.Errorf("noise handshake (%s): not done after %v (right keys and -noise on both peers?)", r.cfg.Direct.Noise, timeout)
	}
	r.res.HandshakeMs = ptr(ms(time.Since(r.t0) - began))
	r.logf("handshake: %s done in %.1f ms", r.cfg.Direct.Noise, *r.res.HandshakeMs)
	return nil
}

func (r *runner) newHandshake() (*noise.Handshake, error) {
	peerKey, err := r.expectedPeerKey()
	if err != nil {
		return nil, err
	}
	initiator, err := r.initiates(peerKey)
	if err != nil {
		return nil, err
	}
	return noise.NewHandshake(noise.HandshakeConfig{
		Pattern:   r.cfg.Direct.Noise,
		Initiator: initiator,
		Static:    r.cfg.Direct.Key,
		PeerKey:   peerKey,
		Prologue:  append([]byte("p2ptestbed direct v2"), wire.U64(r.cfg.Room)...),
	})
}

// expectedPeerKey is the key the peer must prove it holds: provisioned in
// a file, as the rendezvous announced it, or none (trust on first use).
func (r *runner) expectedPeerKey() ([]byte, error) {
	switch d := r.cfg.Direct; d.PeerKeyFrom {
	case "file":
		return r.trustedPeerKey()
	case "rendezvous":
		if r.rdvKey == nil {
			return nil, fmt.Errorf("the rendezvous announced no key for the peer")
		}
		return r.rdvKey, nil
	case "none":
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown peer key source %q", d.PeerKeyFrom)
	}
}

// trustedPeerKey is the trusted key the rendezvous says the peer has. The
// handshake then proves the peer holds it, so a lying rendezvous can only
// make the handshake fail. With one trusted key, the rendezvous is not
// needed at all.
func (r *runner) trustedPeerKey() ([]byte, error) {
	trusted := r.cfg.Direct.TrustedKeys
	if r.rdvKey == nil && len(trusted) == 1 {
		return trusted[0], nil
	}
	for _, k := range trusted {
		if bytes.Equal(k, r.rdvKey) {
			return k, nil
		}
	}
	return nil, fmt.Errorf("the peer's key %x is not one of the %d trusted keys", r.rdvKey, len(trusted))
}

// initiates reports whether we start the handshake. Ordering by static key
// lets any two peers of a mesh agree without being told roles. Under
// -peer-key none the rendezvous's key only sets the order; the handshake
// itself is what authenticates the peer.
func (r *runner) initiates(peerKey []byte) (bool, error) {
	if peerKey == nil {
		peerKey = r.rdvKey
	}
	order := bytes.Compare(r.cfg.Direct.Key.Public, peerKey)
	switch {
	case peerKey == nil:
		return false, errors.New("cannot tell who starts the handshake: no key for the peer from a file or the rendezvous")
	case order == 0:
		return false, errors.New("the peer has our static key (each peer needs its own: punch -genkey)")
	}
	return order < 0, nil
}

func (r *runner) resendHandshake() {
	if last := r.hs.Last(); last != nil && !r.hs.Done() {
		r.sendTo(r.remote, wire.Handshake, 0, last)
	}
}

// onHandshake answers the peer's handshake messages. Before our own
// handshake starts there is nothing to answer with; the initiator retries.
func (r *runner) onHandshake(m inbound) {
	if r.hs == nil || m.hop {
		return
	}
	reply, err := r.hs.Handle(m.pkt.Body)
	if err != nil {
		r.logf("handshake: dropped a bad message from %v: %v", m.from, err)
		return
	}
	if reply != nil {
		r.sendTo(m.from, wire.Handshake, 0, reply)
	}
}
