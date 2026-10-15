package direct

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPatternsComplete(t *testing.T) {
	cases := []struct {
		pattern                    string
		initiatorKey, responderKey []byte // what each side is told about the other
	}{
		{"kk", testKey(2).Public, testKey(1).Public},
		{"ik", testKey(2).Public, testKey(1).Public},
		{"ik", testKey(2).Public, nil}, // responder trusts any initiator on first use
		{"xx", testKey(2).Public, testKey(1).Public},
		{"xx", nil, nil}, // first contact, trust on first use
	}
	for _, c := range cases {
		a, b := sides(t, c.pattern, c.initiatorKey, c.responderKey)
		if err := exchange(a, b); err != nil {
			t.Fatalf("%s: %v", c.pattern, err)
		}
		if !bytes.Equal(a.PeerKey(), testKey(2).Public) || !bytes.Equal(b.PeerKey(), testKey(1).Public) {
			t.Errorf("%s: peers do not know each other's keys", c.pattern)
		}
		if !bytes.Equal(a.Session().HopKey(), b.Session().HopKey()) {
			t.Errorf("%s: hop keys differ", c.pattern)
		}
	}
}

// Every pattern authenticates both sides: a wrong expected key fails it.
func TestWrongKeyFails(t *testing.T) {
	for _, c := range []struct {
		pattern                    string
		initiatorKey, responderKey []byte
	}{
		{"kk", testKey(3).Public, testKey(1).Public}, // initiator expects someone else
		{"kk", testKey(2).Public, testKey(3).Public}, // responder expects someone else
		{"ik", testKey(3).Public, testKey(1).Public},
		{"ik", testKey(2).Public, testKey(3).Public},
		{"xx", testKey(3).Public, nil},
		{"xx", nil, testKey(3).Public},
	} {
		a, b := sides(t, c.pattern, c.initiatorKey, c.responderKey)
		if err := exchange(a, b); err == nil {
			t.Errorf("%s with initiator expecting %x, responder %x: succeeded", c.pattern, c.initiatorKey, c.responderKey)
		}
	}
}

// An impostor must not block the real initiator: the responder rejects the
// impostor's message and still accepts the real one afterwards.
func TestResponderSurvivesImpostor(t *testing.T) {
	for _, pattern := range []string{"ik", "xx"} {
		real, b := sides(t, pattern, testKey(2).Public, testKey(1).Public)
		impostor, err := NewHandshake(HandshakeConfig{Pattern: pattern, Initiator: true, Static: testKey(9),
			PeerKey: testKey(2).Public, Prologue: []byte("trial 1")})
		if err != nil {
			t.Fatal(err)
		}
		if err := exchange(impostor, b); !errors.Is(err, ErrPeerKey) {
			t.Fatalf("%s: impostor got %v, want ErrPeerKey", pattern, err)
		}
		if err := exchange(real, b); err != nil {
			t.Fatalf("%s: real initiator after an impostor: %v", pattern, err)
		}
	}
}

func TestInitiatorGivesUpOnWrongResponder(t *testing.T) {
	a, b := sides(t, "xx", testKey(3).Public, nil)
	if err := exchange(a, b); !errors.Is(err, ErrPeerKey) || a.Failed() == nil {
		t.Fatalf("got %v, failed=%v", err, a.Failed())
	}
}

// Over UDP any message can be lost or arrive twice. Delivering every
// message twice must still finish both sides, and must not make the peers
// echo each other forever.
func TestDuplicatesFinishWithoutEcho(t *testing.T) {
	for _, pattern := range []string{"kk", "ik", "xx"} {
		a, b := sides(t, pattern, testKey(2).Public, testKey(1).Public)
		first, _ := a.Start()
		type delivery struct {
			to  *Handshake
			msg []byte
		}
		queue := []delivery{{b, first}, {b, first}}
		other := map[*Handshake]*Handshake{a: b, b: a}
		steps := 0
		for ; len(queue) > 0 && steps < 50; steps++ {
			d := queue[0]
			queue = queue[1:]
			if reply, err := d.to.Handle(d.msg); err == nil && reply != nil {
				queue = append(queue, delivery{other[d.to], reply}, delivery{other[d.to], reply})
			}
		}
		if steps == 50 {
			t.Fatalf("%s: the peers kept answering each other", pattern)
		}
		if !a.Done() || !b.Done() {
			t.Fatalf("%s: not finished (a %v, b %v)", pattern, a.Done(), b.Done())
		}
	}
}

// In xx the initiator finishes by sending the third message. If that is
// lost, the responder's resent second message must bring it again.
func TestLostFinalMessageXX(t *testing.T) {
	a, b := sides(t, "xx", nil, nil)
	m1, _ := a.Start()
	m2, _ := b.Handle(m1)
	if _, err := a.Handle(m2); err != nil || !a.Done() { // m3 is lost
		t.Fatalf("initiator: %v", err)
	}
	m3, _ := a.Handle(b.Last()) // the responder resends m2
	if _, err := b.Handle(m3); err != nil || !b.Done() {
		t.Fatalf("responder after the resend: %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	for _, c := range []HandshakeConfig{
		{Pattern: "nn", Static: testKey(1)},
		{Pattern: "kk", Static: testKey(1)},                  // kk needs the peer key
		{Pattern: "ik", Initiator: true, Static: testKey(1)}, // so does the ik initiator
		{Pattern: "xx"}, // no static key
	} {
		if _, err := NewHandshake(c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if _, err := NewHandshake(HandshakeConfig{Pattern: "ik", Static: testKey(1)}); err != nil {
		t.Errorf("ik responder without a peer key: %v", err)
	}
}

func TestKeyFiles(t *testing.T) {
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
	os.WriteFile(base+".pub", []byte("abcd\n"), 0o644)
	if _, err := ReadPublicKey(base + ".pub"); err == nil {
		t.Error("short key accepted")
	}
}
