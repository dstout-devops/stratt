package openbao

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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
	// Sign submits a target-generated CSR to /sign/:role — the born-on-target key
	// delivery (ADR-0050): the private key never leaves the target; only the signed
	// cert returns. Replaces Issue (which discarded the server-side key).
	Sign(ctx context.Context, role, csrPEM, ttl string) (Issued, error)
	// Current returns the live cert observed for a commonName (Plan's diff input).
	Current(ctx context.Context, commonName string) (*CurrentCert, error)
	Revoke(ctx context.Context, serial string) (int64, error)
	// GetCA reads the issuing CA cert of the mount (GET /cert/ca) for CA-hierarchy
	// observation (ADR-0098 E2); ok=false when the mount has no CA yet.
	GetCA(ctx context.Context) (pem string, ok bool, err error)
	// RotateCRL rotates the mount's CRL (POST /crl/rotate) — a thin admin call.
	RotateCRL(ctx context.Context) error
	// CreateIntermediate provisions an intermediate CA in intMount, signed under this
	// mount's root, and returns the signed intermediate's serial. Thin OpenBao /pki
	// calls only (never in-process key generation/signing — Stratt is not a CA,
	// ADR-0030). FAILS CLOSED if intMount already has an issuing CA (converge-to-one,
	// never double-mint-reported-green — ADR-0098 §E2/§1.8).
	CreateIntermediate(ctx context.Context, intMount, commonName, ttl string) (caSerial string, err error)
}

// CurrentCert is the live (non-revoked) leaf observed for a commonName — the state
// Plan/Apply decide the reconcile against (ADR-0050).
type CurrentCert struct {
	Serial   string
	NotAfter time.Time
}

// Client is a hand-rolled REST client for a Vault-compatible PKI CLM (dev:
// OpenBao). We deliberately do not vendor an official SDK — the surface is a
// handful of calls and staying hand-rolled keeps the module graph boring (§1.4)
// and the Connector contract sovereign over the transport (§1.5). The same shape
// serves step-ca/Vault/cert-manager behind the cert-issuer contract later.
type Client struct {
	addr         string // e.g. http://localhost:8200
	token        string // X-Vault-Token; spawn-time CredentialRef, never persisted (§2.5)
	mount        string // PKI secrets-engine mount, e.g. "pki"
	transitMount string // Transit secrets-engine mount for KeyCustodian (ADR-0100), e.g. "transit"
	hc           *http.Client
}

// NewClient builds a client. mount defaults to "pki", the Transit mount to "transit".
func NewClient(addr, token, mount string) *Client {
	if mount == "" {
		mount = "pki"
	}
	return &Client{
		addr:         strings.TrimRight(addr, "/"),
		token:        token,
		mount:        mount,
		transitMount: "transit",
		hc:           &http.Client{Timeout: 15 * time.Second},
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

// Cert is one read certificate: its PEM plus revocation state.
type Cert struct {
	Serial    string
	PEM       string
	Revoked   bool
	RevokedAt time.Time // zero when live; the revocation instant otherwise
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
	cert := Cert{Serial: serial, PEM: d.Certificate, Revoked: d.RevocationTime != 0}
	if d.RevocationTime != 0 {
		cert.RevokedAt = time.Unix(d.RevocationTime, 0).UTC()
	}
	return cert, nil
}

// Issued is the result of issuing a leaf certificate — serial, PEM, and the
// expiration epoch. The private key is never captured (§2.5).
type Issued struct {
	Serial     string
	PEM        string
	Expiration int64
}

// Sign submits a target-generated CSR to /sign/:role. Unlike /issue, the CLM does
// NOT generate the keypair — the private key was born on the target and never
// leaves it; only the signed cert (public) returns (ADR-0050, §2.5). Renewal
// re-signs the same CSR/key, so the cert churns but the key is stable.
func (c *Client) Sign(ctx context.Context, role, csrPEM, ttl string) (Issued, error) {
	if ttl == "" {
		ttl = "720h"
	}
	data, err := c.do(ctx, http.MethodPost, "/sign/"+url.PathEscape(role),
		map[string]string{"csr": csrPEM, "ttl": ttl}, false)
	if err != nil {
		return Issued{}, err
	}
	var d issueData
	if err := json.Unmarshal(data, &d); err != nil {
		return Issued{}, fmt.Errorf("clm: decode sign: %w", err)
	}
	return Issued{Serial: d.SerialNumber, PEM: d.Certificate, Expiration: d.Expiration}, nil
}

// Current returns the live (non-revoked) leaf matching commonName with the latest
// notAfter, or nil if none — the observation Plan/Apply reconcile against
// (ADR-0050). O(N) over the CLM's certs (list + read + parse each), like the
// Syncer; a CLM index by CN is the follow-up.
func (c *Client) Current(ctx context.Context, commonName string) (*CurrentCert, error) {
	serials, err := c.ListSerials(ctx)
	if err != nil {
		return nil, err
	}
	var best *CurrentCert
	for _, s := range serials {
		cert, err := c.GetCert(ctx, s)
		if err != nil {
			return nil, err
		}
		if cert.Revoked {
			continue
		}
		crt, err := parseLeaf(cert.PEM)
		if err != nil || crt.IsCA || crt.Subject.CommonName != commonName {
			continue
		}
		if best == nil || crt.NotAfter.After(best.NotAfter) {
			best = &CurrentCert{Serial: s, NotAfter: crt.NotAfter}
		}
	}
	return best, nil
}

// parseLeaf decodes a PEM cert (shared by Current + normalizeCert).
func parseLeaf(pemStr string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("openbao: no PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
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
