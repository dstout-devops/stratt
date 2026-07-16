package planstore

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// fakeDB is an in-memory, write-once ArtifactDB — lets the crypto + content-
// addressing be proven without a database (module isolation for the unit test).
type fakeDB struct{ m map[string][]byte }

func newFakeDB() *fakeDB { return &fakeDB{m: map[string][]byte{}} }

func (f *fakeDB) PutPlanArtifact(_ context.Context, sha string, ct []byte) error {
	if _, exists := f.m[sha]; exists {
		return nil // write-once: first ciphertext wins (content-addressed idempotency)
	}
	f.m[sha] = append([]byte(nil), ct...)
	return nil
}

func (f *fakeDB) GetPlanArtifact(_ context.Context, sha string) ([]byte, error) {
	ct, ok := f.m[sha]
	if !ok {
		return nil, ErrNotFound
	}
	return ct, nil
}

// testKey is 32 bytes of hex (64 chars).
const testKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestPlanStore_RoundTripContentAddressedAndEncrypted(t *testing.T) {
	db := newFakeDB()
	s, err := New(testKey, db)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	plan := []byte(`{"resource":"aws_instance.web","secret":"hunter2"}`)
	digest, err := s.Put(context.Background(), plan)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	// Content-addressed: the digest is the sha256 of the PLAINTEXT.
	if len(digest) != 64 {
		t.Fatalf("digest must be a 64-char hex sha256, got %q", digest)
	}
	// Encrypted at rest: the stored bytes must NOT contain the plan secret in clear.
	if bytes.Contains(db.m[digest], []byte("hunter2")) {
		t.Fatal("plan secret must not be stored in the clear (§2.5)")
	}
	// GetVerified returns the exact plaintext after a fetch-and-rehash.
	got, err := s.GetVerified(context.Background(), digest)
	if err != nil {
		t.Fatalf("getVerified: %v", err)
	}
	if !bytes.Equal(got, plan) {
		t.Fatalf("roundtrip mismatch: %q", got)
	}
}

func TestPlanStore_WriteOnceIdempotent(t *testing.T) {
	db := newFakeDB()
	s, _ := New(testKey, db)
	plan := []byte("same plan")
	d1, _ := s.Put(context.Background(), plan)
	first := append([]byte(nil), db.m[d1]...)
	d2, _ := s.Put(context.Background(), plan)
	if d1 != d2 {
		t.Fatalf("same plan must content-address to the same digest: %s vs %s", d1, d2)
	}
	// Write-once: the second Put must not replace the first ciphertext (immutable).
	if !bytes.Equal(first, db.m[d1]) {
		t.Fatal("a fixed digest must never be re-pointed at different bytes (immutable)")
	}
}

func TestPlanStore_TamperDetected(t *testing.T) {
	db := newFakeDB()
	s, _ := New(testKey, db)
	digest, _ := s.Put(context.Background(), []byte("trustworthy plan"))
	// Swap the bytes behind the fixed digest — GetVerified must DETECT it, never
	// serve mutated bytes as authentic (a corrupt AEAD open, or a hash mismatch).
	db.m[digest] = []byte("evil ciphertext that will not decrypt")
	_, err := s.GetVerified(context.Background(), digest)
	if err == nil {
		t.Fatal("mutated plan bytes must be rejected at the Apply boundary (verify-don't-trust)")
	}
}

func TestPlanStore_MissingFailsClosed(t *testing.T) {
	db := newFakeDB()
	s, _ := New(testKey, db)
	_, err := s.GetVerified(context.Background(), "deadbeef")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing pinned plan must surface ErrNotFound (fail closed), got %v", err)
	}
}

func TestPlanStore_BadKeyRejected(t *testing.T) {
	if _, err := New("tooshort", newFakeDB()); err == nil {
		t.Fatal("a non-32-byte key must be rejected")
	}
}
