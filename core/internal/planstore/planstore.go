// Package planstore is the content-addressed, encrypted, immutable artifact store
// for saved actuator plans (ADR-0047 §8, Actuator slice 3). A plan is
// secret-bearing (a tofu plan embeds resolved values), so it may NOT live in
// evidencestore (WORM/object-locked *compliance* retention, plaintext at rest,
// and a Named Kind meaning a Finding's backing). Instead the plan is:
//
//   - keyed by the sha256 of its PLAINTEXT (content-addressed — the digest IS the
//     address, and it is what a Gate binds and a human approves, §1.8);
//   - encrypted at rest (AES-256-GCM, the statebackend class, ADR-0016) so the
//     platform store never holds plan secrets in the clear (§2.5);
//   - write-once / immutable (ON CONFLICT DO NOTHING; a fixed digest can never be
//     re-pointed at different bytes);
//   - fetched-and-re-hashed by the CORE at the Apply boundary (GetVerified) —
//     verify-don't-trust (§1.5), never a plugin re-reading its own plan.
//
// The core computing the digest over an OPAQUE plan Payload is content-blind
// (invariant #1 untouched — the core never interprets the plan); the plaintext
// transits core memory only to be hashed/encrypted and is never held as core
// state beyond this encrypted store.
package planstore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/dstout-devops/stratt/core/internal/keycustodian"
)

// ArtifactDB is the persistence seam (graph.Store satisfies it). Kept narrow so
// the crypto + content-addressing logic is unit-testable without a database.
type ArtifactDB interface {
	// PutPlanArtifact stores ciphertext under the plaintext digest, WRITE-ONCE:
	// a second write of the same digest is a no-op (idempotent, content-addressed).
	PutPlanArtifact(ctx context.Context, sha256Hex string, ciphertext []byte) error
	// GetPlanArtifact returns the stored ciphertext for a digest, or ErrNotFound.
	GetPlanArtifact(ctx context.Context, sha256Hex string) ([]byte, error)
}

// ErrTampered signals a stored plan whose decrypted plaintext no longer hashes to
// its content-address — the backend-independent immutability guarantee (§1.8: a
// mutated plan is DETECTED on read, never applied as authentic).
var ErrTampered = errors.New("planstore: plan artifact failed integrity check")

// ErrNotFound signals no plan stored under a digest (a pinned Apply whose plan is
// missing must fail closed, never fall back to an unpinned apply — ADR-0047 §8).
var ErrNotFound = errors.New("planstore: no plan artifact for digest")

// Store encrypts and content-addresses saved plans.
type Store struct {
	db ArtifactDB
	// aead is the KEK-derived AES-GCM, retained for the LEGACY read path (plans
	// written before ADR-0100); new writes go through envelope encryption.
	aead      cipher.AEAD
	custodian keycustodian.Custodian // in-core envelope floor (ADR-0100)
	domain    string
}

// New builds the store from a 32-byte hex key (the STRATT_STATE_KEY class, reused
// so plan artifacts share the state-encryption posture — one key to hold, ADR-0016).
func New(hexKey string, db ArtifactDB) (*Store, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("planstore: key must be 32 bytes of hex (64 chars)")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	custodian, err := keycustodian.NewLocal(key)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, aead: aead, custodian: custodian, domain: "default"}, nil
}

// UseCustodian replaces the envelope custodian (ADR-0100), like statebackend's — the
// legacy read path and content-addressing are unaffected.
func (s *Store) UseCustodian(c keycustodian.Custodian) { s.custodian = c }

// Put content-addresses and stores a plan, returning its digest (the pin a Gate
// binds). The digest is over the PLAINTEXT plan, so the pin anchors plan content,
// not the (nonce-randomized) ciphertext; write-once means re-Putting the same
// plan is idempotent and keeps the first ciphertext.
func (s *Store) Put(ctx context.Context, plan []byte) (string, error) {
	sum := sha256.Sum256(plan)
	digest := hex.EncodeToString(sum[:])
	ct, err := s.encrypt(ctx, plan)
	if err != nil {
		return "", fmt.Errorf("planstore: encrypt: %w", err)
	}
	if err := s.db.PutPlanArtifact(ctx, digest, ct); err != nil {
		return "", fmt.Errorf("planstore: put %s: %w", digest, err)
	}
	return digest, nil
}

// GetVerified fetches the plan at digest, decrypts it, and RE-HASHES the plaintext
// against the digest — the core's own verification at the Apply boundary
// (verify-don't-trust, §1.5). A mismatch is ErrTampered; a missing plan is
// ErrNotFound (both terminal for a pinned Apply — never a silent unpinned apply).
func (s *Store) GetVerified(ctx context.Context, digest string) ([]byte, error) {
	ct, err := s.db.GetPlanArtifact(ctx, digest)
	if err != nil {
		return nil, err // ErrNotFound propagates
	}
	plan, err := s.decrypt(ctx, ct)
	if err != nil {
		return nil, fmt.Errorf("planstore: decrypt %s: %w", digest, err)
	}
	sum := sha256.Sum256(plan)
	if got := hex.EncodeToString(sum[:]); got != digest {
		return nil, fmt.Errorf("%w: digest %s but plaintext hashes to %s", ErrTampered, digest, got)
	}
	return plan, nil
}

// encrypt envelope-encrypts a plan (ADR-0100): per-blob DEK + custodian-wrapped key.
func (s *Store) encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	return keycustodian.Seal(ctx, s.custodian, s.domain, plaintext)
}

// decrypt opens an enveloped plan, falling back to the LEGACY bare-AES read path for
// plans stored before ADR-0100 (the content-address re-hash in GetVerified is unchanged).
func (s *Store) decrypt(ctx context.Context, blob []byte) ([]byte, error) {
	pt, enveloped, err := keycustodian.Open(ctx, s.custodian, blob)
	if err != nil {
		return nil, err
	}
	if enveloped {
		return pt, nil
	}
	n := s.aead.NonceSize()
	if len(blob) < n {
		return nil, fmt.Errorf("planstore: ciphertext too short")
	}
	return s.aead.Open(nil, blob[:n], blob[n:], nil)
}
