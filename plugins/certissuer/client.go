package certissuer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CA is the small slice of the Vault-compatible PKI CLM this plugin needs — the
// enumeration/read pair for the Syncer (ListSerials, GetCert) and the write ops
// for the multi-op Action (Issue, Revoke). Abstracting it behind an interface lets
// tests inject a fake with no live CLM (the ADR-0046 module-isolation proof: the
// plugin's content-expertise is exercised in isolation). *Client satisfies it.
type CA interface {
	ListSerials(ctx context.Context) ([]string, error)
	GetCert(ctx context.Context, serial string) (Cert, error)
	Issue(ctx context.Context, role, commonName, ttl string) (Issued, error)
	Revoke(ctx context.Context, serial string) (int64, error)
}

// Client is a hand-rolled REST client for a Vault-compatible PKI CLM (dev:
// OpenBao). We deliberately do not vendor an official SDK — the surface is a
// handful of calls and staying hand-rolled keeps the module graph boring (§1.4)
// and the Connector contract sovereign over the transport (§1.5). The same shape
// serves step-ca/Vault/cert-manager behind the cert-issuer contract later.
type Client struct {
	addr  string // e.g. http://localhost:8200
	token string // X-Vault-Token; spawn-time CredentialRef, never persisted (§2.5)
	mount string // secrets-engine mount, e.g. "pki"
	hc    *http.Client
}

// NewClient builds a PKI client. mount defaults to "pki".
func NewClient(addr, token, mount string) *Client {
	if mount == "" {
		mount = "pki"
	}
	return &Client{
		addr:  strings.TrimRight(addr, "/"),
		token: token,
		mount: mount,
		hc:    &http.Client{Timeout: 15 * time.Second},
	}
}

// certData is the shape of a single cert read (GET .../cert/:serial). A
// revocation_time of 0 means the cert is live; non-zero is the revoke epoch.
type certData struct {
	Certificate    string `json:"certificate"`
	RevocationTime int64  `json:"revocation_time"`
}

// issueData is the shape of an issue response (POST .../issue/:role). The
// private_key field is deliberately NOT modeled — the plugin never reads it, so
// it can never cross the wire (§2.5).
type issueData struct {
	SerialNumber string `json:"serial_number"`
	Certificate  string `json:"certificate"`
	Expiration   int64  `json:"expiration"`
}

// revokeData is the shape of a revoke response (POST .../revoke).
type revokeData struct {
	RevocationTime int64 `json:"revocation_time"`
}

// vaultResp wraps every PKI response; only .data is load-bearing here.
type vaultResp struct {
	Data json.RawMessage `json:"data"`
	// Errors carries CLM-side error strings on 4xx/5xx.
	Errors []string `json:"errors"`
}

// do issues one request and decodes the .data envelope. list=true is appended
// for enumeration (Vault/OpenBao's GET-as-LIST form).
func (c *Client) do(ctx context.Context, method, path string, body any, list bool) (json.RawMessage, error) {
	u := c.addr + "/v1/" + c.mount + path
	if list {
		u += "?list=true"
	}
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var vr vaultResp
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &vr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if len(vr.Errors) > 0 {
			return nil, fmt.Errorf("clm: %s %s: %d: %s", method, path, resp.StatusCode, strings.Join(vr.Errors, "; "))
		}
		return nil, fmt.Errorf("clm: %s %s: status %d", method, path, resp.StatusCode)
	}
	return vr.Data, nil
}

// ListSerials returns every issued cert serial (colon-hex), including the CA
// and any revoked certs — normalizeCert / the Syncer filter those.
func (c *Client) ListSerials(ctx context.Context) ([]string, error) {
	data, err := c.do(ctx, http.MethodGet, "/certs", nil, true)
	if err != nil {
		return nil, err
	}
	var d struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("clm: decode cert list: %w", err)
	}
	return d.Keys, nil
}

// Cert is one read certificate: its PEM plus whether it is revoked.
type Cert struct {
	Serial  string
	PEM     string
	Revoked bool
}

// GetCert fetches one cert by serial (colon-hex).
func (c *Client) GetCert(ctx context.Context, serial string) (Cert, error) {
	data, err := c.do(ctx, http.MethodGet, "/cert/"+url.PathEscape(serial), nil, false)
	if err != nil {
		return Cert{}, err
	}
	var d certData
	if err := json.Unmarshal(data, &d); err != nil {
		return Cert{}, fmt.Errorf("clm: decode cert %s: %w", serial, err)
	}
	return Cert{Serial: serial, PEM: d.Certificate, Revoked: d.RevocationTime != 0}, nil
}

// Issued is the result of issuing a leaf certificate — serial, PEM, and the
// expiration epoch. The private key is never captured (§2.5).
type Issued struct {
	Serial     string
	PEM        string
	Expiration int64
}

// Issue mints a new leaf certificate for commonName under role with the given
// TTL (e.g. "720h"). Used by the certissuer/issue and certissuer/renew Actions.
func (c *Client) Issue(ctx context.Context, role, commonName, ttl string) (Issued, error) {
	if ttl == "" {
		ttl = "720h"
	}
	data, err := c.do(ctx, http.MethodPost, "/issue/"+url.PathEscape(role),
		map[string]string{"common_name": commonName, "ttl": ttl}, false)
	if err != nil {
		return Issued{}, err
	}
	var d issueData
	if err := json.Unmarshal(data, &d); err != nil {
		return Issued{}, fmt.Errorf("clm: decode issue: %w", err)
	}
	return Issued{Serial: d.SerialNumber, PEM: d.Certificate, Expiration: d.Expiration}, nil
}

// Revoke revokes a certificate by serial (colon-hex) and returns the CLM's
// revocation epoch. Idempotent at the CLM — revoking an already-revoked cert is
// a no-op.
func (c *Client) Revoke(ctx context.Context, serial string) (int64, error) {
	data, err := c.do(ctx, http.MethodPost, "/revoke",
		map[string]string{"serial_number": serial}, false)
	if err != nil {
		return 0, err
	}
	var d revokeData
	if len(data) > 0 {
		if err := json.Unmarshal(data, &d); err != nil {
			return 0, fmt.Errorf("clm: decode revoke: %w", err)
		}
	}
	return d.RevocationTime, nil
}
