package keycustodian

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"
)

func mkKEK(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return k
}

// TestSealOpenRoundTrip proves envelope encryption: Seal → Open returns the plaintext,
// and the blob is a self-describing envelope (not the plaintext, not bare AES).
func TestSealOpenRoundTrip(t *testing.T) {
	c, err := NewLocal(mkKEK(t))
	if err != nil {
		t.Fatal(err)
	}
	pt := []byte("terraform state: super secret")
	blob, err := Seal(context.Background(), c, "default", pt)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if !IsEnveloped(blob) {
		t.Fatal("Seal must produce an enveloped blob")
	}
	if bytes.Contains(blob, pt) {
		t.Fatal("the plaintext must not appear in the ciphertext")
	}
	got, enveloped, err := Open(context.Background(), c, blob)
	if err != nil || !enveloped {
		t.Fatalf("open: enveloped=%v err=%v", enveloped, err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("round-trip mismatch: %q", got)
	}
}

// TestOpenLegacyIsNotEnveloped proves the migration path: a legacy bare-AES blob is
// reported enveloped=false (no error) so the caller falls back to its legacy read path.
func TestOpenLegacyIsNotEnveloped(t *testing.T) {
	c, _ := NewLocal(mkKEK(t))
	legacy := []byte{0x1, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7, 0x8, 0x9, 0xa, 0xb, 0xc, 0xff} // random-ish, no magic
	pt, enveloped, err := Open(context.Background(), c, legacy)
	if err != nil || enveloped || pt != nil {
		t.Fatalf("legacy blob must be enveloped=false,nil: enveloped=%v err=%v pt=%v", enveloped, err, pt)
	}
}

// TestUnwrapWrongProviderFailsClosed proves a DEK wrapped by another provider is refused
// (residency/eject discipline — a custodian only unwraps what it owns).
func TestUnwrapWrongProviderFailsClosed(t *testing.T) {
	c, _ := NewLocal(mkKEK(t))
	// A wrapped key claiming a different provider must not unwrap.
	foreign := []byte(`{"p":"openbao-transit","d":"india","v":3,"w":"AAAA"}`)
	if _, err := c.(*localCustodian).Unwrap(context.Background(), foreign); err == nil {
		t.Fatal("local custodian must refuse a foreign-provider wrapped DEK (fail closed)")
	}
}

// TestTamperFailsClosed proves a mutated envelope fails the AEAD (§1.8 tamper-evidence).
func TestTamperFailsClosed(t *testing.T) {
	c, _ := NewLocal(mkKEK(t))
	blob, _ := Seal(context.Background(), c, "default", []byte("x"))
	blob[len(blob)-1] ^= 0xff // flip a ciphertext byte
	if _, _, err := Open(context.Background(), c, blob); err == nil {
		t.Fatal("a tampered envelope must fail to open")
	}
}

// TestCrossKEKFailsClosed proves a blob sealed under one KEK can't open under another
// (each custody floor is isolated — the basis of per-Cell sovereignty).
func TestCrossKEKFailsClosed(t *testing.T) {
	a, _ := NewLocal(mkKEK(t))
	b, _ := NewLocal(mkKEK(t))
	blob, _ := Seal(context.Background(), a, "default", []byte("secret"))
	if _, _, err := Open(context.Background(), b, blob); err == nil {
		t.Fatal("a blob sealed under KEK-A must not open under KEK-B")
	}
}
