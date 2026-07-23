// Package statebackend is strattd's OpenTofu HTTP state backend (charter §8
// Phase 2, ADR-0016): GET/POST plus LOCK/UNLOCK per workspace, with state
// ENCRYPTED AT REST (AES-256-GCM) before it touches the store. Mounted
// outside /api/v1 — execution pods authenticate with a per-workspace HMAC
// credential, not a Principal.
package statebackend

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/keycustodian"
)

// Backend serves and protects OpenTofu state.
type Backend struct {
	store *graph.Store
	log   *slog.Logger
	// aead is the KEK-derived AES-GCM, retained for the LEGACY read path (bare
	// AES-GCM state written before ADR-0100). New writes go through envelope
	// encryption (custodian); reads fall back to aead only for un-enveloped blobs.
	aead      cipher.AEAD
	custodian keycustodian.Custodian // the in-core floor (envelope encryption, ADR-0100)
	domain    string                 // custody domain (a Cell); "default" until multi-Cell threads the Cell id
	key       []byte
}

// New builds a Backend from a 32-byte hex key (STRATT_STATE_KEY).
func New(hexKey string, store *graph.Store, log *slog.Logger) (*Backend, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("statebackend: STRATT_STATE_KEY must be 32 bytes of hex (64 chars)")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// The in-core KeyCustodian floor (ADR-0100): the state key becomes the KEK that
	// wraps per-blob DEKs. No external service; compiled-in, in-process.
	custodian, err := keycustodian.NewLocal(key)
	if err != nil {
		return nil, err
	}
	// Key separation: the credential-MAC key is derived from (not equal to)
	// the state-encryption key, so neither use weakens the other
	// (charter-guardian hygiene note, ADR-0016).
	credMAC := hmac.New(sha256.New, key)
	credMAC.Write([]byte("stratt/statebackend/credential/v1"))
	return &Backend{
		store: store, log: log.With("component", "statebackend"),
		aead: aead, custodian: custodian, domain: "default", key: credMAC.Sum(nil),
	}, nil
}

// UseCustodian replaces the envelope custodian (ADR-0100): strattd injects a mux over
// the local floor + an optional KMS-backed provider when one is configured for a domain.
// The legacy bare-AES read path (aead) is unchanged, so pre-envelope blobs still decrypt.
func (b *Backend) UseCustodian(c keycustodian.Custodian) { b.custodian = c }

// WorkspaceCredential derives the per-workspace pod credential:
// hex(HMAC-SHA256(key, workspace)). Stateless to verify, scoped to one
// workspace, delivered to pods as TF_HTTP_PASSWORD (never in files).
// Dev-grade — hardening is a recorded ADR-0016 follow-up.
func (b *Backend) WorkspaceCredential(workspace string) string {
	mac := hmac.New(sha256.New, b.key)
	mac.Write([]byte(workspace))
	return hex.EncodeToString(mac.Sum(nil))
}

func (b *Backend) authorized(r *http.Request, workspace string) bool {
	_, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	want := b.WorkspaceCredential(workspace)
	return subtle.ConstantTimeCompare([]byte(pass), []byte(want)) == 1
}

// encrypt envelope-encrypts state (ADR-0100): a per-blob DEK does the AES-256-GCM, the
// in-core custodian wraps the DEK. Same at-rest guarantee, now KMS-pluggable.
func (b *Backend) encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	return keycustodian.Seal(ctx, b.custodian, b.domain, plaintext)
}

// decrypt opens an enveloped blob via the custodian, falling back to the LEGACY bare
// AES-GCM read path for state written before ADR-0100 (migration: legacy reads through,
// the next write re-seals it as an envelope).
func (b *Backend) decrypt(ctx context.Context, blob []byte) ([]byte, error) {
	pt, enveloped, err := keycustodian.Open(ctx, b.custodian, blob)
	if err != nil {
		return nil, err
	}
	if enveloped {
		return pt, nil
	}
	n := b.aead.NonceSize()
	if len(blob) < n {
		return nil, fmt.Errorf("statebackend: ciphertext too short")
	}
	return b.aead.Open(nil, blob[:n], blob[n:], nil)
}

// Handler serves /statebackend/{workspace}.
func (b *Backend) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workspace := strings.Trim(strings.TrimPrefix(r.URL.Path, "/statebackend/"), "/")
		if workspace == "" || strings.Contains(workspace, "/") {
			http.Error(w, "workspace required", http.StatusBadRequest)
			return
		}
		if !b.authorized(r, workspace) {
			w.Header().Set("WWW-Authenticate", `Basic realm="stratt-state"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := r.Context()
		switch r.Method {
		case http.MethodGet:
			data, _, err := b.store.GetOpenTofuState(ctx, workspace)
			if err != nil {
				b.fail(w, err)
				return
			}
			if len(data) == 0 {
				http.Error(w, "no state", http.StatusNotFound)
				return
			}
			plain, err := b.decrypt(ctx, data)
			if err != nil {
				b.fail(w, fmt.Errorf("decrypt %s: %w", workspace, err))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(plain)

		case http.MethodPost:
			// tofu passes its lock ID on writes; a write that doesn't hold
			// the current lock is refused (423 + holder document).
			ok, holder, err := b.lockCheck(ctx, workspace, r.URL.Query().Get("ID"))
			if err != nil {
				b.fail(w, err)
				return
			}
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusLocked)
				_, _ = w.Write(holder)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
			if err != nil {
				b.fail(w, err)
				return
			}
			enc, err := b.encrypt(ctx, body)
			if err != nil {
				b.fail(w, err)
				return
			}
			if err := b.store.PutOpenTofuState(ctx, workspace, enc); err != nil {
				b.fail(w, err)
				return
			}
			w.WriteHeader(http.StatusOK)

		case "LOCK":
			lockDoc, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				b.fail(w, err)
				return
			}
			held, holder, err := b.store.LockOpenTofuState(ctx, workspace, lockDoc)
			if err != nil {
				b.fail(w, err)
				return
			}
			if held {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusLocked)
				_, _ = w.Write(holder)
				return
			}
			w.WriteHeader(http.StatusOK)

		case "UNLOCK":
			if err := b.store.UnlockOpenTofuState(ctx, workspace); err != nil {
				b.fail(w, err)
				return
			}
			w.WriteHeader(http.StatusOK)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// lockCheck verifies a state POST holds the current lock. Unlocked
// workspaces accept writes (tofu -lock=false and first writes); a held lock
// requires a matching ID.
func (b *Backend) lockCheck(ctx context.Context, workspace, id string) (bool, []byte, error) {
	_, lock, err := b.store.GetOpenTofuState(ctx, workspace)
	if err != nil {
		return false, nil, err
	}
	if len(lock) == 0 {
		return true, nil, nil
	}
	var doc struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(lock, &doc); err == nil && doc.ID == id && id != "" {
		return true, nil, nil
	}
	return false, lock, nil
}

func (b *Backend) fail(w http.ResponseWriter, err error) {
	b.log.Error("state backend error", "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
