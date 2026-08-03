package openbao

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Transit is the slice of OpenBao Transit the KeyCustodian capability needs (ADR-0100):
// wrap/unwrap a DEK under a KEK that NEVER leaves OpenBao. A per-domain key is the
// per-Cell sovereignty seam. Abstracted for test injection; *Client satisfies it.
type Transit interface {
	EnsureKey(ctx context.Context, key string) error
	Encrypt(ctx context.Context, key string, plaintext []byte) (ciphertext []byte, keyVersion int, err error)
	Decrypt(ctx context.Context, key string, ciphertext []byte) (plaintext []byte, err error)
	// HMAC computes a MAC over data under key (ADR-0164 D2) — the operation that lets the core
	// verify an inbound signature without ever holding the key.
	HMAC(ctx context.Context, key, algorithm string, data []byte) ([]byte, error)
}

// transitKey derives the Transit key name for a custody domain (per-domain KEK).
func transitKey(domain string) string {
	if domain == "" {
		domain = "default"
	}
	return "stratt-" + domain
}

// EnsureKey creates the Transit key if absent (idempotent — OpenBao no-ops an existing).
func (c *Client) EnsureKey(ctx context.Context, key string) error {
	_, err := c.doPath(ctx, http.MethodPost, c.transitMount+"/keys/"+key, map[string]any{"type": "aes256-gcm96"})
	return err
}

// Encrypt wraps plaintext (the DEK) under the Transit key, returning the opaque Vault
// ciphertext + the key version that wrapped it.
func (c *Client) Encrypt(ctx context.Context, key string, plaintext []byte) ([]byte, int, error) {
	data, err := c.doPath(ctx, http.MethodPost, c.transitMount+"/encrypt/"+key,
		map[string]string{"plaintext": base64.StdEncoding.EncodeToString(plaintext)})
	if err != nil {
		return nil, 0, err
	}
	var d struct {
		Ciphertext string `json:"ciphertext"`
		KeyVersion int    `json:"key_version"`
	}
	if err := json.Unmarshal(data, &d); err != nil || d.Ciphertext == "" {
		return nil, 0, fmt.Errorf("transit: malformed encrypt response")
	}
	return []byte(d.Ciphertext), d.KeyVersion, nil
}

// Decrypt unwraps the Vault ciphertext back to the DEK.
func (c *Client) Decrypt(ctx context.Context, key string, ciphertext []byte) ([]byte, error) {
	data, err := c.doPath(ctx, http.MethodPost, c.transitMount+"/decrypt/"+key,
		map[string]string{"ciphertext": string(ciphertext)})
	if err != nil {
		return nil, err
	}
	var d struct {
		Plaintext string `json:"plaintext"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("transit: malformed decrypt response")
	}
	return base64.StdEncoding.DecodeString(d.Plaintext)
}

// WrapKey is the KeyCustodian wrap RPC (ADR-0100): wrap a DEK for a custody domain via
// OpenBao Transit. The KEK never leaves OpenBao; only the opaque wrapped bytes return.
func (s *Server) WrapKey(ctx context.Context, req *pluginv1.WrapKeyRequest) (*pluginv1.WrapKeyResponse, error) {
	tr, err := s.newTransit(ctx)
	if err != nil {
		return nil, err
	}
	key := transitKey(req.GetDomain())
	if err := tr.EnsureKey(ctx, key); err != nil {
		return nil, status.Errorf(codes.Internal, "wrapkey: ensure transit key: %v", err)
	}
	ct, ver, err := tr.Encrypt(ctx, key, req.GetDek())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "wrapkey: %v", err)
	}
	return &pluginv1.WrapKeyResponse{Wrapped: ct, KeyVersion: int32(ver)}, nil
}

// UnwrapKey is the KeyCustodian unwrap RPC: unwrap a DEK for its custody domain.
func (s *Server) UnwrapKey(ctx context.Context, req *pluginv1.UnwrapKeyRequest) (*pluginv1.UnwrapKeyResponse, error) {
	tr, err := s.newTransit(ctx)
	if err != nil {
		return nil, err
	}
	dek, err := tr.Decrypt(ctx, transitKey(req.GetDomain()), req.GetWrapped())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unwrapkey: %v", err)
	}
	return &pluginv1.UnwrapKeyResponse{Dek: dek}, nil
}

// ── MACVerifier (ADR-0164 D2) ───────────────────────────────────────────────────────────────────
//
// The core cannot verify an inbound webhook signature: it would need the shared secret, and
// ADR-0052 keeps material out of the control plane. Transit already holds keys and computes HMACs,
// so the question travels here and only a boolean travels back. THE KEY NEVER LEAVES THE KMS.

// hmacAlgorithms maps the declared MAC onto Transit's own spelling. A closed set on purpose: an
// algorithm this provider cannot compute is a REFUSAL, never a silent downgrade to a weaker one.
var hmacAlgorithms = map[string]string{
	"hmac-sha256": "sha2-256",
	"hmac-sha512": "sha2-512",
}

// VerifyMAC asks Transit for the HMAC of body under keyRef and compares it to what the caller
// presented. The comparison is constant-time, and the response says only yes or no — a caller who
// could learn WHY a signature was rejected learns something about the key.
func (s *Server) VerifyMAC(ctx context.Context, req *pluginv1.VerifyMACRequest) (*pluginv1.VerifyMACResponse, error) {
	algo, ok := hmacAlgorithms[req.GetAlgorithm()]
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument,
			"openbao: %q is not a MAC this provider computes (have: hmac-sha256, hmac-sha512)", req.GetAlgorithm())
	}
	if req.GetKeyRef() == "" {
		return nil, status.Error(codes.InvalidArgument, "openbao: VerifyMAC needs a key coordinate")
	}
	tr, err := s.newTransit(ctx)
	if err != nil {
		return nil, err
	}
	want, err := tr.HMAC(ctx, req.GetKeyRef(), algo, req.GetBody())
	if err != nil {
		// The question could not be ASKED. Distinct from "answered no", and the ingest door
		// keeps them apart: one refuses this caller, the other refuses everyone until an
		// operator acts (§1.8).
		return nil, status.Errorf(codes.Unavailable, "openbao: HMAC for key %q: %v", req.GetKeyRef(), err)
	}
	return &pluginv1.VerifyMACResponse{
		Valid: subtle.ConstantTimeCompare(want, req.GetSignature()) == 1,
	}, nil
}

// HMAC returns the MAC of data under a Transit key. Transit answers "vault:v1:<base64>"; the
// caller wants raw bytes to compare against what a source sent.
func (c *Client) HMAC(ctx context.Context, key, algorithm string, data []byte) ([]byte, error) {
	raw, err := c.doPath(ctx, http.MethodPost, c.transitMount+"/hmac/"+key+"/"+algorithm,
		map[string]string{"input": base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		return nil, err
	}
	var d struct {
		HMAC string `json:"hmac"`
	}
	if err := json.Unmarshal(raw, &d); err != nil || d.HMAC == "" {
		return nil, fmt.Errorf("transit: malformed hmac response")
	}
	// "vault:v1:<base64>" — the version prefix is Transit's, not the source's.
	b64 := d.HMAC
	if i := strings.LastIndex(b64, ":"); i >= 0 {
		b64 = b64[i+1:]
	}
	out, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("transit: hmac is not base64: %w", err)
	}
	return out, nil
}
