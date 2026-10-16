package identity

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestKeyFiles(t *testing.T) {
	peer, _ := FromPrivate(bytes.Repeat([]byte{2}, KeySize))
	base := filepath.Join(t.TempDir(), "keys", "a")
	kp, err := NewKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteKeypair(base, kp); err != nil {
		t.Fatal(err)
	}
	got, err := ReadKeypair(base + ".key")
	if err != nil || !bytes.Equal(got.Private, kp.Private) || !bytes.Equal(got.Public, kp.Public) {
		t.Fatalf("read back %v: %v", got, err)
	}
	pub, err := ReadPublicKey(base + ".pub")
	if err != nil || !bytes.Equal(pub, kp.Public) {
		t.Fatalf("public key: %v", err)
	}
	if info, _ := os.Stat(base + ".key"); info.Mode().Perm() != 0o600 {
		t.Errorf("private key mode %v, want 0600", info.Mode().Perm())
	}
	os.WriteFile(filepath.Join(filepath.Dir(base), "b.pub"), []byte(hex.EncodeToString(peer.Public)), 0o644)
	trusted, err := ReadTrustedKeys(filepath.Dir(base), kp.Public)
	if err != nil || len(trusted) != 1 || !bytes.Equal(trusted[0], peer.Public) {
		t.Fatalf("trusted keys from the directory, without our own: %x, %v", trusted, err)
	}
	if _, err := ReadTrustedKeys(base+".pub", kp.Public); err == nil {
		t.Error("only our own key accepted as a trusted set")
	}
	os.WriteFile(base+".pub", []byte("abcd\n"), 0o644)
	if _, err := ReadPublicKey(base + ".pub"); err == nil {
		t.Error("short key accepted")
	}
}
