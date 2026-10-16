// Package noise secures the direct channel: a Noise handshake (kk, ik or xx)
// over the punched socket, then an encrypted UDP transport with explicit
// nonces and a replay window. The session also derives the hop key that
// package direct builds its port schedule from.
package noise

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"p2ptestbed/p2p/internal/identity"

	flynn "github.com/flynn/noise"
)

var suite = flynn.NewCipherSuite(flynn.DH25519, flynn.CipherChaChaPoly, flynn.HashBLAKE2s)

// Patterns, by what the peers know of each other beforehand:
//
//	kk  both know each other's static key   (peers already in a network)
//	ik  the initiator knows the responder's (WireGuard's choice; 1 round trip)
//	xx  neither: keys travel in the handshake (first contact; 1.5 round trips)
//
// All three give mutual authentication and forward secrecy. With xx, and
// with ik on the responder side, the peer's key is checked against
// PeerKey after the handshake reveals it; without a PeerKey it is trusted
// on first use.
var patterns = map[string]flynn.HandshakePattern{
	"kk": flynn.HandshakeKK,
	"ik": flynn.HandshakeIK,
	"xx": flynn.HandshakeXX,
}

// HandshakeConfig for one side.
type HandshakeConfig struct {
	Pattern   string // kk, ik or xx
	Initiator bool
	Static    identity.Keypair
	PeerKey   []byte // expected peer public key; required for kk and the ik initiator
	Prologue  []byte // the same on both sides; names the trial so handshakes cannot be replayed into another
}

func (c HandshakeConfig) validate() error {
	if _, ok := patterns[c.Pattern]; !ok {
		return fmt.Errorf("direct: unknown handshake pattern %q (want kk, ik or xx)", c.Pattern)
	}
	if len(c.Static.Private) != identity.KeySize || len(c.Static.Public) != identity.KeySize {
		return errors.New("direct: a static keypair is required")
	}
	if c.PeerKey == nil && (c.Pattern == "kk" || c.Pattern == "ik" && c.Initiator) {
		return fmt.Errorf("direct: pattern %s needs the peer's public key", c.Pattern)
	}
	return nil
}

// Handshake builds and checks handshake messages; the caller carries them
// over the punched socket. Both sides resend Last() while not Done, and
// answer a repeated message from the peer with their own last message
// again (the reply was probably lost). Not safe for concurrent use.
type Handshake struct {
	cfg     HandshakeConfig
	hs      *flynn.HandshakeState
	lastIn  []byte // the peer's last message, to recognise repeats
	last    []byte // our last message
	session *Session
	failed  error
}

func NewHandshake(cfg HandshakeConfig) (*Handshake, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	h := &Handshake{cfg: cfg}
	return h, h.reset()
}

// reset starts this side over, keeping its configuration.
func (h *Handshake) reset() error {
	var peer []byte
	if h.cfg.Pattern == "kk" || h.cfg.Pattern == "ik" && h.cfg.Initiator {
		peer = h.cfg.PeerKey // these patterns use it inside the handshake
	}
	hs, err := flynn.NewHandshakeState(flynn.Config{
		CipherSuite:   suite,
		Random:        rand.Reader,
		Pattern:       patterns[h.cfg.Pattern],
		Initiator:     h.cfg.Initiator,
		Prologue:      h.cfg.Prologue,
		StaticKeypair: flynn.DHKey{Private: h.cfg.Static.Private, Public: h.cfg.Static.Public},
		PeerStatic:    peer,
	})
	h.hs, h.lastIn, h.last = hs, nil, nil
	return err
}

// Start returns the initiator's first message.
func (h *Handshake) Start() ([]byte, error) {
	if !h.cfg.Initiator {
		return nil, errors.New("direct: only the initiator starts the handshake")
	}
	if h.last != nil {
		return h.last, nil
	}
	return h.write()
}

// Handle processes one message from the peer and returns the reply to send,
// if any. An error means the message was rejected: invalid (flynn/noise
// rolls its state back), or from an unexpected key.
func (h *Handshake) Handle(msg []byte) ([]byte, error) {
	switch {
	case h.lastIn != nil && bytes.Equal(msg, h.lastIn):
		return h.last, nil // a repeat: our answer to it was probably lost
	case h.session != nil || h.failed != nil:
		return nil, nil
	}
	if _, cs1, cs2, err := h.hs.ReadMessage(nil, msg); err != nil {
		return nil, err
	} else if err := h.checkPeer(); err != nil {
		return nil, err
	} else if cs1 != nil {
		// The final message: nothing of ours answers it, so a repeat of it
		// gets no reply (answering with an earlier message would make the
		// peers echo each other forever).
		h.last = nil
		return nil, h.finish(msg, cs1, cs2)
	}
	h.lastIn = append([]byte(nil), msg...)
	return h.write()
}

// checkPeer compares the peer's key, once the handshake has revealed it,
// with the expected one. A responder starts over, so an impostor cannot
// block the real initiator; an initiator gives up.
func (h *Handshake) checkPeer() error {
	got := h.hs.PeerStatic()
	if len(got) == 0 || h.cfg.PeerKey == nil || sameKey(got, h.cfg.PeerKey) {
		return nil
	}
	if !h.cfg.Initiator {
		return errors.Join(ErrPeerKey, h.reset())
	}
	h.failed = ErrPeerKey
	return ErrPeerKey
}

func (h *Handshake) write() ([]byte, error) {
	out, cs1, cs2, err := h.hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, err
	}
	h.last = out
	if cs1 != nil {
		err = h.finish(nil, cs1, cs2)
	}
	return out, err
}

func (h *Handshake) finish(lastIn []byte, cs1, cs2 *flynn.CipherState) error {
	if lastIn != nil {
		h.lastIn = append([]byte(nil), lastIn...)
	}
	var err error
	h.session, err = newSession(cs1, cs2, h.cfg.Initiator)
	return err
}

// Last is the message to resend while waiting.
func (h *Handshake) Last() []byte { return h.last }

// Done reports whether this side has its session. The side that sends the
// final message is done before knowing it arrived, so it keeps answering
// repeats through Handle.
func (h *Handshake) Done() bool { return h.session != nil }

func (h *Handshake) Initiator() bool { return h.cfg.Initiator }

// Failed is the reason this side gave up, or nil.
func (h *Handshake) Failed() error { return h.failed }

func (h *Handshake) Session() *Session { return h.session }

// PeerKey is the peer's static key, once the handshake has it.
func (h *Handshake) PeerKey() []byte { return h.hs.PeerStatic() }

var ErrPeerKey = errors.New("direct: the peer's static key is not the expected one")

func sameKey(a, b []byte) bool { return len(a) == identity.KeySize && bytes.Equal(a, b) }
