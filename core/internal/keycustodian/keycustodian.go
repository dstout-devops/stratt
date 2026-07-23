// Package keycustodian is the KeyCustodian capability + envelope encryption (ADR-0100):
// state-at-rest is always encrypted in-process with a per-blob data key (DEK); a
// Custodian only WRAPS/UNWRAPS the DEK, never the data. The built-in localCustodian
// wraps under a local KEK and requires NO external service — the self-sufficient floor
// the spine encrypts itself with (§1.4). Optional providers (OpenBao Transit, cloud
// KMS) implement the same Custodian over the plugin port; the wrapped DEK is
// SELF-DESCRIBING ({domain, provider, keyVersion}) so it is residency-bound,
// partition-detectable, and rewrappable-to-local (§1.3 eject; §1.2 domain binding).
package keycustodian

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
)

// Custodian wraps a data key (DEK) for a custody domain and unwraps it back. The wrapped
// bytes are self-describing (see wrappedKey) so any Custodian that owns the domain can
// unwrap them, and a domain can be migrated between providers (rewrap-to-local).
type Custodian interface {
	// Wrap encrypts dek for domain, returning self-describing wrapped bytes.
	Wrap(ctx context.Context, domain string, dek []byte) ([]byte, error)
	// Unwrap decrypts wrapped bytes produced by a Custodian of this provider, returning
	// the DEK. It fails closed on a provider/domain it does not own.
	Unwrap(ctx context.Context, wrapped []byte) ([]byte, error)
	// Identity is the provider identity stamped into wrapped DEKs (e.g. "local").
	Identity() string
}

// wrappedKey is the self-describing wrapped-DEK envelope (ADR-0100 §4): the provider
// that wrapped it, the custody domain, the KEK version, and the provider-specific
// wrapped bytes. Small (wraps a 32-byte DEK) — JSON is fine.
type wrappedKey struct {
	Provider   string `json:"p"`
	Domain     string `json:"d"`
	KeyVersion int    `json:"v"`
	Wrapped    []byte `json:"w"`
}

// magic prefixes an enveloped blob so the read path distinguishes it from legacy
// bare-AES-GCM state (a random 12-byte GCM nonce matching this 12-byte magic is ~2^-96).
var magic = []byte("STRATTKCENV1")

// Seal envelope-encrypts plaintext: a fresh DEK does the in-process AES-256-GCM, the
// Custodian wraps the DEK, and the blob is magic || len(wrapped) || wrapped || dataCT.
// The DEK is zeroized before return.
func Seal(ctx context.Context, c Custodian, domain string, plaintext []byte) ([]byte, error) {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, err
	}
	defer zero(dek)
	dataCT, err := gcmSeal(dek, plaintext)
	if err != nil {
		return nil, err
	}
	wrapped, err := c.Wrap(ctx, domain, dek)
	if err != nil {
		return nil, fmt.Errorf("keycustodian: wrap dek (domain %q): %w", domain, err)
	}
	var buf bytes.Buffer
	buf.Write(magic)
	var lb [binary.MaxVarintLen64]byte
	buf.Write(lb[:binary.PutUvarint(lb[:], uint64(len(wrapped)))])
	buf.Write(wrapped)
	buf.Write(dataCT)
	return buf.Bytes(), nil
}

// IsEnveloped reports whether blob is a KeyCustodian envelope (vs legacy bare-AES).
func IsEnveloped(blob []byte) bool { return bytes.HasPrefix(blob, magic) }

// Open decrypts an enveloped blob: unwrap the DEK via the Custodian, then AES-GCM-open
// the data. Returns enveloped=false (no error) for a legacy blob so the caller can fall
// back to its bare-AES read path (migration). The DEK is zeroized before return.
func Open(ctx context.Context, c Custodian, blob []byte) (plaintext []byte, enveloped bool, err error) {
	if !IsEnveloped(blob) {
		return nil, false, nil
	}
	rest := blob[len(magic):]
	wlen, n := binary.Uvarint(rest)
	if n <= 0 || uint64(len(rest)-n) < wlen {
		return nil, true, fmt.Errorf("keycustodian: malformed envelope header")
	}
	wrapped := rest[n : n+int(wlen)]
	dataCT := rest[n+int(wlen):]
	dek, err := c.Unwrap(ctx, wrapped)
	if err != nil {
		return nil, true, fmt.Errorf("keycustodian: unwrap dek: %w", err)
	}
	defer zero(dek)
	pt, err := gcmOpen(dek, dataCT)
	if err != nil {
		return nil, true, fmt.Errorf("keycustodian: decrypt: %w", err)
	}
	return pt, true, nil
}

// ── localCustodian: the in-core floor (no external service) ──────────────────

// localCustodian wraps the DEK under a local KEK with AES-256-GCM. It is compiled-in and
// in-process — NEVER reached over the plugin port — so the spine self-encrypts with zero
// external services (ADR-0100 §2). The KEK is today's STRATT_STATE_KEY.
type localCustodian struct {
	kekAEAD cipher.AEAD
	version int
}

// NewLocal builds the in-core floor Custodian from a 32-byte KEK.
func NewLocal(kek []byte) (Custodian, error) {
	if len(kek) != 32 {
		return nil, fmt.Errorf("keycustodian: local KEK must be 32 bytes")
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &localCustodian{kekAEAD: aead, version: 1}, nil
}

func (l *localCustodian) Identity() string { return "local" }

func (l *localCustodian) Wrap(_ context.Context, domain string, dek []byte) ([]byte, error) {
	ct, err := gcmSealAEAD(l.kekAEAD, dek)
	if err != nil {
		return nil, err
	}
	return json.Marshal(wrappedKey{Provider: "local", Domain: domain, KeyVersion: l.version, Wrapped: ct})
}

func (l *localCustodian) Unwrap(_ context.Context, wrapped []byte) ([]byte, error) {
	var wk wrappedKey
	if err := json.Unmarshal(wrapped, &wk); err != nil {
		return nil, fmt.Errorf("keycustodian: malformed wrapped key: %w", err)
	}
	if wk.Provider != "local" {
		return nil, fmt.Errorf("keycustodian: local custodian cannot unwrap a %q-wrapped DEK (domain %q)", wk.Provider, wk.Domain)
	}
	return gcmOpenAEAD(l.kekAEAD, wk.Wrapped)
}

// ── AES-256-GCM helpers (nonce || ciphertext) ────────────────────────────────

func gcmSeal(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcmSealAEAD(aead, plaintext)
}

func gcmOpen(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcmOpenAEAD(aead, ciphertext)
}

func gcmSealAEAD(aead cipher.AEAD, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, aead.Seal(nil, nonce, plaintext, nil)...), nil
}

func gcmOpenAEAD(aead cipher.AEAD, ciphertext []byte) ([]byte, error) {
	n := aead.NonceSize()
	if len(ciphertext) < n {
		return nil, fmt.Errorf("keycustodian: ciphertext too short")
	}
	return aead.Open(nil, ciphertext[:n], ciphertext[n:], nil)
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
