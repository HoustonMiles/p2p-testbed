package noise

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"sync"
	"sync/atomic"

	flynn "github.com/flynn/noise"

	"golang.org/x/crypto/hkdf"
)

// Transport packet (big-endian):
//
//	type (1) | receiver index (u32) | counter (u64) | ciphertext + tag
//
// The receiver index names the session the packet is for, as in WireGuard,
// so one socket can serve several sessions (links of a mesh whose
// schedules pick the same port). The counter is sent explicitly because
// Noise's implicit nonces fall out of step after the first lost or
// reordered datagram. The whole header is authenticated.
//
// TODO(paper §5 Threat Model, attacker position): the index stays the same
// for the whole session, so an on-path observer can follow one link across
// hops by it (as it already can by the addresses). Rotate it with the hop
// key if Experiment C shows that matters.
const (
	indexOffset   = 1
	counterOffset = 5
	headerLen     = 13
	tagLen        = 16
	Overhead      = headerLen + tagLen
)

// Noise reserves nonce 2^64-1; packets never use it, the hop key does.
const maxCounter = math.MaxUint64 - 1

var (
	ErrShort     = errors.New("noise: packet too short")
	ErrAuth      = errors.New("noise: authentication failed")
	ErrReplay    = errors.New("noise: replayed or too old")
	ErrExhausted = errors.New("noise: nonce space exhausted")
)

// Session seals and opens transport packets; safe for concurrent use.
//
// TODO(paper §5 Procedures, long sessions): rekey before the counter runs
// out (Noise REKEY), and rotate the hop key with it.
type Session struct {
	send, recv flynn.Cipher
	counter    atomic.Uint64

	mu     sync.Mutex
	replay replayWindow

	hopKey                []byte
	initiator             bool
	localIndex, peerIndex uint32
}

// newSession takes the handshake's cipher states: cs1 encrypts
// initiator -> responder, cs2 the other way.
func newSession(cs1, cs2 *flynn.CipherState, initiator bool) (*Session, error) {
	i2r, r2i := cs1.Cipher(), cs2.Cipher()
	s := &Session{send: i2r, recv: r2i, initiator: initiator}
	if !initiator {
		s.send, s.recv = r2i, i2r
	}
	key, err := deriveHopKey(i2r)
	s.hopKey = key
	s.localIndex, s.peerIndex = sessionIndex(key, initiator), sessionIndex(key, !initiator)
	return s, err
}

// sessionIndex derives a side's receiver index from the hop key, so the
// peers agree on both indexes without sending them.
func sessionIndex(hopKey []byte, initiator bool) uint32 {
	info := []byte("receiver index r")
	if initiator {
		info[len(info)-1] = 'i'
	}
	var out [4]byte
	io.ReadFull(hkdf.Expand(sha256.New, hopKey, info), out[:])
	return binary.BigEndian.Uint32(out[:])
}

// LocalIndex is the receiver index on packets sent to us.
func (s *Session) LocalIndex() uint32 { return s.localIndex }

// ReceiverIndex reads the receiver index of a transport packet, before any
// crypto, to find its session.
func ReceiverIndex(pkt []byte) (uint32, bool) {
	if len(pkt) < Overhead {
		return 0, false
	}
	return binary.BigEndian.Uint32(pkt[indexOffset:counterOffset]), true
}

// deriveHopKey encrypts zeros under the reserved nonce (as Noise's REKEY
// does) and runs the result through HKDF. Only the two peers hold the
// cipher key, the result is fresh per handshake (ephemeral keys), and the
// hop schedule stays independent of the packet keys. The handshake hash
// would not do: it is computed from the public transcript.
func deriveHopKey(i2r flynn.Cipher) ([]byte, error) {
	block := i2r.Encrypt(nil, math.MaxUint64, []byte("p2ptestbed hop key"), make([]byte, 32))
	key := make([]byte, 32)
	_, err := io.ReadFull(hkdf.New(sha256.New, block, nil, []byte("p2ptestbed hop key v1")), key)
	return key, err
}

// HopKey is the secret both peers derive the hop schedule from.
func (s *Session) HopKey() []byte { return s.hopKey }

// Initiator reports whether this side started the handshake.
func (s *Session) Initiator() bool { return s.initiator }

// Seal appends an encrypted packet to dst, which must not overlap payload.
func (s *Session) Seal(dst []byte, typ byte, payload []byte) ([]byte, error) {
	n := s.counter.Add(1) - 1
	if n > maxCounter {
		return nil, ErrExhausted
	}
	start := len(dst)
	dst = append(dst, typ)
	dst = binary.BigEndian.AppendUint32(dst, s.peerIndex)
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
	n := binary.BigEndian.Uint64(pkt[counterOffset:headerLen])
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
