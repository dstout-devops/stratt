package openbao

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

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
