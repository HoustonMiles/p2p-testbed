// Package direct is the Port-Hopping Direct Channel: a Noise handshake over
// the punched socket, then an encrypted UDP transport whose ports follow a
// schedule derived from the session keys.
//
//   - keys.go       long-lived static keypairs and key files
//   - handshake.go  the Noise handshake (kk, ik or xx)
//   - noise.go      the transport: sealing, opening, replay window, hop key
//   - schedule.go   which port each peer uses in each time slot
//   - hopper.go     the sockets that follow the schedule
package direct

import (
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

// Packet types. The type travels in the clear but is authenticated as part
// of the associated data.
const (
	TypeData  byte = 1
	TypeProbe byte = 2 // body: slot (u64), flags (u8)
)

// Transport packet: type (1) | counter (u64, big-endian) | ciphertext + tag.
// The counter is sent explicitly, as in WireGuard, because Noise's implicit
// nonces fall out of step after the first lost or reordered datagram.
const (
	headerLen = 9
	tagLen    = 16
	Overhead  = headerLen + tagLen
)

// Noise reserves nonce 2^64-1; packets never use it, the hop key does.
const maxCounter = math.MaxUint64 - 1

var (
	ErrShort     = errors.New("direct: packet too short")
	ErrAuth      = errors.New("direct: authentication failed")
	ErrReplay    = errors.New("direct: replayed or too old")
	ErrExhausted = errors.New("direct: nonce space exhausted")
)

// Session seals and opens transport packets; safe for concurrent use.
//
// TODO(paper §5 Procedures, long sessions): rekey before the counter runs
// out (Noise REKEY), and rotate the hop key with it.
type Session struct {
	send, recv noise.Cipher
	counter    atomic.Uint64

	mu     sync.Mutex
	replay replayWindow

	hopKey    []byte
	initiator bool
}

// newSession takes the handshake's cipher states: cs1 encrypts
// initiator -> responder, cs2 the other way.
func newSession(cs1, cs2 *noise.CipherState, initiator bool) (*Session, error) {
	i2r, r2i := cs1.Cipher(), cs2.Cipher()
	s := &Session{send: i2r, recv: r2i, initiator: initiator}
	if !initiator {
		s.send, s.recv = r2i, i2r
	}
	key, err := deriveHopKey(i2r)
	s.hopKey = key
	return s, err
}

// deriveHopKey encrypts zeros under the reserved nonce (as Noise's REKEY
// does) and runs the result through HKDF. Only the two peers hold the
// cipher key, the result is fresh per handshake (ephemeral keys), and the
// hop schedule stays independent of the packet keys. The handshake hash
// would not do: it is computed from the public transcript.
func deriveHopKey(i2r noise.Cipher) ([]byte, error) {
	block := i2r.Encrypt(nil, math.MaxUint64, []byte("p2ptestbed hop key"), make([]byte, 32))
	key := make([]byte, 32)
	_, err := io.ReadFull(hkdf.New(sha256.New, block, nil, []byte("p2ptestbed hop key v1")), key)
	return key, err
}

// HopKey is the secret both peers derive the hop schedule from.
func (s *Session) HopKey() []byte { return s.hopKey }

// sides returns our side of the link and the peer's.
func (s *Session) sides() (local, peer byte) {
	if s.initiator {
		return SideInitiator, SideResponder
	}
	return SideResponder, SideInitiator
}

// Seal appends an encrypted packet to dst, which must not overlap payload.
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

// Open authenticates and decrypts pkt into dst, which must not overlap pkt.
// The replay window is checked before decrypting, so replayed floods cost
// no crypto, and again after, because another reader may have accepted the
// same counter meanwhile.
func (s *Session) Open(dst, pkt []byte) (typ byte, payload []byte, err error) {
	if len(pkt) < Overhead {
		return 0, nil, ErrShort
	}
	n := binary.BigEndian.Uint64(pkt[1:headerLen])
	if n > maxCounter {
		return 0, nil, ErrAuth
	}
	if !s.replayOK(n, false) {
		return 0, nil, ErrReplay
	}
	payload, err = s.recv.Decrypt(dst, n, pkt[:headerLen], pkt[headerLen:])
	if err != nil {
		return 0, nil, ErrAuth
	}
	if !s.replayOK(n, true) {
		return 0, nil, ErrReplay
	}
	return pkt[0], payload, nil
}

// replayOK checks counter n against the window and, with accept, records it.
func (s *Session) replayOK(n uint64, accept bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.replay.check(n) {
		return false
	}
	if accept {
		s.replay.update(n)
	}
	return true
}

// replayWindow remembers the last windowBits counters in a ring bitmap
// (RFC 6479); within the window packets may arrive in any order.
const windowBits = 2048

type replayWindow struct {
	next uint64 // highest accepted counter + 1
	bits [windowBits / 64]uint64
}

func (w *replayWindow) check(n uint64) bool {
	if n >= w.next {
		return true
	}
	if w.next-n > windowBits {
		return false
	}
	i := n % windowBits
	return w.bits[i/64]&(1<<(i%64)) == 0
}

// update marks n accepted; call only after check(n).
func (w *replayWindow) update(n uint64) {
	if n >= w.next {
		w.advance(n)
	}
	i := n % windowBits
	w.bits[i/64] |= 1 << (i % 64)
}

// advance moves the window's top to n, clearing the ring positions that
// held counters now out of the window.
func (w *replayWindow) advance(n uint64) {
	if n-w.next+1 >= windowBits {
		w.bits = [windowBits / 64]uint64{}
	} else {
		for c := w.next; c <= n; c++ {
			i := c % windowBits
			w.bits[i/64] &^= 1 << (i % 64)
		}
	}
	w.next = n + 1
}
