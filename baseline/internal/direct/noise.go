// Package direct is the encrypted, port-hopping direct channel: the path under
// test, compared against the raw UDP baseline (package peer) and the TURN relay.
//
// It has three parts:
//
//   - noise.go     Noise handshake (NNpsk0) and the UDP transport built on it
//   - schedule.go  the shared, clock-based hop schedule (which port, when)
//   - hopper.go    the sockets: open the next slot's port, probe the peer's,
//     send and receive on the current one, close the old one
//
// The hole punch itself is not here: it is done by package peer as before.
// The handshake runs over that punched socket, then the Hopper takes over on
// new ports derived from the handshake's keys.
package direct

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"sync"
	"sync/atomic"

	"github.com/flynn/noise"
	"golang.org/x/crypto/hkdf"
)

// ---------------------------------------------------------------------------
// Handshake
// ---------------------------------------------------------------------------

// Noise_NNpsk0_25519_ChaChaPoly_BLAKE2s. Two messages:
//
//	-> psk, e
//	<- e, ee
//
// NN alone has no authentication, so the rendezvous server (which forwards
// the addresses) could sit in the middle. The pre-shared key, given to both
// peers by the experiment runner, makes the handshake fail for anyone who
// does not know it. Ephemeral keys on both sides give forward secrecy.
var suite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)

// PSKSize is the required pre-shared key length.
const PSKSize = 32

// Handshake is one side of the Noise handshake. It only builds and checks
// messages; the caller moves them over the punched socket (for example as the
// body of a wire packet) and retransmits Last() until the handshake is Done.
// That keeps the handshake on the same single socket as the punch, which is
// what the NAT mapping belongs to.
//
// Not safe for concurrent use.
type Handshake struct {
	hs        *noise.HandshakeState
	initiator bool
	first     []byte // responder: the initiator's message, to spot retransmits
	last      []byte // the last message we sent, for retransmission
	session   *Session
}

// NewHandshake starts a handshake. Role 'a' is the initiator, 'b' the
// responder. The prologue is mixed into the handshake hash, so both sides
// must pass the same bytes; use something that names the trial (e.g. the
// session id) so a handshake cannot be replayed into another trial.
func NewHandshake(initiator bool, psk, prologue []byte) (*Handshake, error) {
	if len(psk) != PSKSize {
		return nil, errors.New("direct: psk must be 32 bytes")
	}
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:           suite,
		Random:                rand.Reader,
		Pattern:               noise.HandshakeNN,
		Initiator:             initiator,
		Prologue:              prologue,
		PresharedKey:          psk,
		PresharedKeyPlacement: 0,
	})
	if err != nil {
		return nil, err
	}
	return &Handshake{hs: hs, initiator: initiator}, nil
}

// Start returns the initiator's first message. Only the initiator calls it.
func (h *Handshake) Start() ([]byte, error) {
	if !h.initiator {
		return nil, errors.New("direct: only the initiator starts the handshake")
	}
	if h.last != nil {
		return h.last, nil
	}
	msg, _, _, err := h.hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, err
	}
	h.last = msg
	return msg, nil
}

// Handle processes one handshake message from the peer and returns the
// reply to send, if any. An error means the message was not valid (wrong
// PSK, corrupted, forged); drop it and keep waiting. flynn/noise rolls the
// state back on a failed read, so a bad message does not spoil the handshake.
//
// Duplicates are expected over UDP: a responder that sees the first message
// again answers with its reply again (the first reply was probably lost), and
// an initiator ignores repeated replies.
func (h *Handshake) Handle(msg []byte) (reply []byte, err error) {
	if h.session != nil {
		if !h.initiator && bytes.Equal(msg, h.first) {
			return h.last, nil
		}
		return nil, nil
	}
	if h.initiator {
		if h.last == nil {
			return nil, errors.New("direct: Start must be called first")
		}
		// <- e, ee: this completes the handshake.
		_, cs1, cs2, err := h.hs.ReadMessage(nil, msg)
		if err != nil {
			return nil, err
		}
		h.session, err = newSession(cs1, cs2, true)
		return nil, err
	}
	// Responder. -> psk, e
	if _, _, _, err := h.hs.ReadMessage(nil, msg); err != nil {
		return nil, err
	}
	// <- e, ee: writing our reply completes the handshake on this side.
	out, cs1, cs2, err := h.hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, err
	}
	h.first = append([]byte(nil), msg...)
	h.last = out
	h.session, err = newSession(cs1, cs2, false)
	return out, err
}

// Last is the message to retransmit while waiting (nil before Start).
func (h *Handshake) Last() []byte { return h.last }

// Done reports whether the handshake has finished on this side.
//
// The responder is done as soon as it sends its reply, before it knows the
// reply arrived. It learns that from the initiator's first transport packet;
// until then it must keep answering retransmitted first messages via Handle.
func (h *Handshake) Done() bool { return h.session != nil }

// Session returns the transport session, or nil until Done.
func (h *Handshake) Session() *Session { return h.session }

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

// Packet types. The type is sent in the clear but authenticated (it is part
// of the associated data), so it cannot be changed in flight.
const (
	TypeData  byte = 1 // application payload
	TypeProbe byte = 2 // opens a NAT pinhole for a hop slot; body: slot (u64), flags (u8)
)

// Transport packet layout:
//
//	0      1                 9
//	| type | counter (u64 BE) | ciphertext + 16-byte tag ...
//
// The counter is the AEAD nonce, sent explicitly (as in WireGuard) because
// UDP loses and reorders packets. Noise's own CipherState keeps an implicit
// counter on each side, which falls out of step after the first lost packet.
const (
	headerLen = 9
	tagLen    = 16
	Overhead  = headerLen + tagLen // bytes added to every payload
)

// Noise reserves nonce 2^64-1, so it is never used for a packet. We use it
// once, to derive the hop key (see newSession).
const maxCounter = math.MaxUint64 - 1

var (
	ErrShort     = errors.New("direct: packet too short")
	ErrAuth      = errors.New("direct: authentication failed")
	ErrReplay    = errors.New("direct: replayed or too old")
	ErrExhausted = errors.New("direct: nonce space exhausted")
)

// Session encrypts and decrypts transport packets after the handshake.
// Seal and Open are safe for concurrent use.
type Session struct {
	send, recv noise.Cipher
	counter    atomic.Uint64 // next nonce to send

	mu     sync.Mutex
	replay replayWindow

	hopKey []byte
}

// newSession takes the two cipher states from the handshake. cs1 encrypts
// initiator -> responder, cs2 responder -> initiator.
func newSession(cs1, cs2 *noise.CipherState, initiator bool) (*Session, error) {
	i2r, r2i := cs1.Cipher(), cs2.Cipher()
	s := &Session{send: i2r, recv: r2i}
	if !initiator {
		s.send, s.recv = r2i, i2r
	}

	// Hop key: encrypt 32 zero bytes with the initiator->responder key under
	// the reserved nonce 2^64-1 (the same trick Noise's REKEY uses), then run
	// the result through HKDF. Both sides hold that key, nobody else does,
	// and it is fresh for every handshake. Using a separate derivation keeps
	// the hop schedule independent of the packet encryption keys.
	//
	// (The handshake hash, ChannelBinding(), would not do: with NNpsk0 it
	// depends only on the PSK and public values, not on the DH secret.)
	block := i2r.Encrypt(nil, math.MaxUint64, []byte("p2ptestbed hop key"), make([]byte, 32))
	s.hopKey = make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, block, nil, []byte("p2ptestbed hop key v1")), s.hopKey); err != nil {
		return nil, err
	}
	return s, nil
}

// HopKey is the secret both peers derive the hop schedule from.
func (s *Session) HopKey() []byte { return s.hopKey }

// Seal appends an encrypted packet of type typ carrying payload to dst.
// dst must not overlap payload.
func (s *Session) Seal(dst []byte, typ byte, payload []byte) ([]byte, error) {
	n := s.counter.Add(1) - 1
	if n > maxCounter {
		return nil, ErrExhausted
	}
	start := len(dst)
	dst = append(dst, typ)
	dst = binary.BigEndian.AppendUint64(dst, n)
	return s.send.Encrypt(dst, n, dst[start:start+headerLen], payload), nil
}

// Open authenticates and decrypts pkt, appending the payload to dst (which
// must not overlap pkt). Forged, corrupted, replayed and too-old packets are
// rejected with an error; the caller just drops them.
func (s *Session) Open(dst, pkt []byte) (typ byte, payload []byte, err error) {
	if len(pkt) < Overhead {
		return 0, nil, ErrShort
	}
	typ = pkt[0]
	n := binary.BigEndian.Uint64(pkt[1:headerLen])
	if n > maxCounter {
		return 0, nil, ErrAuth
	}

	// Cheap replay check first, so replayed floods cost no decryption...
	s.mu.Lock()
	ok := s.replay.check(n)
	s.mu.Unlock()
	if !ok {
		return 0, nil, ErrReplay
	}
	payload, err = s.recv.Decrypt(dst, n, pkt[:headerLen], pkt[headerLen:])
	if err != nil {
		return 0, nil, ErrAuth
	}
	// ...and the real one after, since another goroutine may have accepted
	// the same counter while we were decrypting.
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.replay.check(n) {
		return 0, nil, ErrReplay
	}
	s.replay.update(n)
	return typ, payload, nil
}

// replayWindow remembers which of the last windowBits counters have been
// accepted (RFC 6479 style ring bitmap). Packets may arrive in any order
// within the window; anything older is rejected.
const windowBits = 2048

type replayWindow struct {
	next uint64                  // highest accepted counter + 1 (0: none yet)
	bits [windowBits / 64]uint64 // bit (n % windowBits) set = n accepted
}

func (w *replayWindow) check(n uint64) bool {
	if n >= w.next {
		return true // newer than anything seen
	}
	if w.next-n > windowBits {
		return false // fell out of the window
	}
	i := n % windowBits
	return w.bits[i/64]&(1<<(i%64)) == 0
}

// update marks n accepted. Call only after check(n) returned true.
func (w *replayWindow) update(n uint64) {
	if n >= w.next {
		// Moving the window forward: forget the counters that the ring
		// positions between the old top and n held before.
		gap := n - w.next + 1
		if gap >= windowBits {
			w.bits = [windowBits / 64]uint64{}
		} else {
			for c := w.next; c <= n; c++ {
				i := c % windowBits
				w.bits[i/64] &^= 1 << (i % 64)
			}
		}
		w.next = n + 1
	}
	i := n % windowBits
	w.bits[i/64] |= 1 << (i % 64)
}

