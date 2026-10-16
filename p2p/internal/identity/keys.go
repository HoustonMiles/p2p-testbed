// Package identity is a peer's long-lived static keys and the files they
// live in: its own keypair, and the public keys of the peers it trusts.
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/curve25519"
)

const KeySize = 32

// Keypair is a peer's long-lived X25519 identity.
type Keypair struct {
	Private, Public []byte
}

func NewKeypair() (Keypair, error) {
	priv := make([]byte, KeySize)
	if _, err := rand.Read(priv); err != nil {
		return Keypair{}, err
	}
	return FromPrivate(priv)
}

// FromPrivate rebuilds a keypair from its private half.
func FromPrivate(priv []byte) (Keypair, error) {
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	return Keypair{Private: priv, Public: pub}, err
}

// WriteKeypair writes <base>.key (private, readable by the owner only) and
// <base>.pub, each as one line of hex.
func WriteKeypair(base string, kp Keypair) error {
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(base+".key", []byte(hex.EncodeToString(kp.Private)+"\n"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(base+".pub", []byte(hex.EncodeToString(kp.Public)+"\n"), 0o644)
}

// ReadKeypair reads a .key file; the public half is derived from it.
func ReadKeypair(path string) (Keypair, error) {
	priv, err := readHexKey(path)
	if err != nil {
		return Keypair{}, err
	}
	return FromPrivate(priv)
}

// ReadPublicKey reads a .pub file.
func ReadPublicKey(path string) ([]byte, error) { return readHexKey(path) }

func readHexKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) != KeySize {
		return nil, fmt.Errorf("%s: want %d bytes of hex", path, KeySize)
	}
	return key, nil
}
