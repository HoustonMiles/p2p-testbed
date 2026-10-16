package direct

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
	return keypairFrom(priv)
}

func keypairFrom(priv []byte) (Keypair, error) {
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
	return keypairFrom(priv)
}

// ReadPublicKey reads a .pub file.
func ReadPublicKey(path string) ([]byte, error) { return readHexKey(path) }

// ReadTrustedKeys reads one .pub file, or every .pub file in a directory:
// the members of a network. The key except (our own, in a shared
// directory) is left out.
func ReadTrustedKeys(path string, except []byte) ([][]byte, error) {
	paths := []string{path}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		paths, _ = filepath.Glob(filepath.Join(path, "*.pub"))
	}
	var keys [][]byte
	for _, p := range paths {
		key, err := ReadPublicKey(p)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(key, except) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%s: no public keys of other peers", path)
	}
	return keys, nil
}

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

var ErrPeerKey = errors.New("direct: the peer's static key is not the expected one")

func sameKey(a, b []byte) bool { return len(a) == KeySize && bytes.Equal(a, b) }
