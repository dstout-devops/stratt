package openbao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// doPath is do() with an EXPLICIT /v1 path (not mount-prefixed) — for cross-mount and
// sys/mounts calls the CA-admin surface needs (ADR-0098 E2). Same envelope decoding.
func (c *Client) doPath(ctx context.Context, method, v1path string, body any) (json.RawMessage, error) {
	u := c.addr + "/v1/" + strings.TrimPrefix(v1path, "/")
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
			return nil, fmt.Errorf("clm: %s %s: %d: %s", method, v1path, resp.StatusCode, strings.Join(vr.Errors, "; "))
		}
		return nil, fmt.Errorf("clm: %s %s: status %d", method, v1path, resp.StatusCode)
	}
	return vr.Data, nil
}

// GetCA reads the issuing CA cert of this plugin's mount (ADR-0098 E2). ok=false when
// the mount has no CA (benign for observation — the Syncer just projects no ca Entity).
func (c *Client) GetCA(ctx context.Context) (string, bool, error) {
	return c.caOfMount(ctx, c.mount)
}

// caOfMount reads a mount's CA cert. A read error or empty cert ⇒ ok=false (no CA).
func (c *Client) caOfMount(ctx context.Context, mount string) (string, bool, error) {
	data, err := c.doPath(ctx, http.MethodGet, mount+"/cert/ca", nil)
	if err != nil {
		// A mount with no CA (or not enabled) is "no CA", not a hard failure here.
		return "", false, nil
	}
	var d struct {
		Certificate string `json:"certificate"`
	}
	if err := json.Unmarshal(data, &d); err != nil || strings.TrimSpace(d.Certificate) == "" {
		return "", false, nil
	}
	return d.Certificate, true, nil
}

// RotateCRL rotates this mount's CRL (GET /crl/rotate — a thin admin call, ADR-0098 E2).
func (c *Client) RotateCRL(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/crl/rotate", nil, false)
	return err
}

// CreateIntermediate provisions an intermediate CA in intMount signed under this mount's
// root, returning the signed intermediate's serial. Thin OpenBao /pki calls only —
// NEVER in-process key generation/signing (Stratt integrates the CLM, never becomes a
// CA — ADR-0030). FAILS CLOSED if intMount already has an issuing CA (converge-to-one,
// §1.8 — a re-invoke does not mint a second intermediate and report green).
func (c *Client) CreateIntermediate(ctx context.Context, intMount, commonName, ttl string) (string, error) {
	if intMount == "" {
		intMount = "pki_int"
	}
	if ttl == "" {
		ttl = "43800h" // 5y
	}
	// Fail closed: an existing issuing CA in intMount is NOT overwritten.
	if _, exists, _ := c.caOfMount(ctx, intMount); exists {
		return "", fmt.Errorf("openbao: intermediate mount %q already has an issuing CA — refusing to mint a second (fail-closed, ADR-0098)", intMount)
	}
	// Enable the intermediate mount if absent (idempotent-ish: a 400 "already in use"
	// is tolerated — the CA check above already guarded double-mint).
	if _, err := c.doPath(ctx, http.MethodPost, "sys/mounts/"+intMount, map[string]any{"type": "pki"}); err != nil {
		if !strings.Contains(err.Error(), "already in use") && !strings.Contains(err.Error(), "path is already") {
			return "", fmt.Errorf("openbao: enable intermediate mount: %w", err)
		}
	}
	// 1. Generate the intermediate CSR (private key stays inside OpenBao's int mount).
	csrData, err := c.doPath(ctx, http.MethodPost, intMount+"/intermediate/generate/internal",
		map[string]string{"common_name": commonName, "ttl": ttl})
	if err != nil {
		return "", fmt.Errorf("openbao: generate intermediate CSR: %w", err)
	}
	var csr struct {
		CSR string `json:"csr"`
	}
	if err := json.Unmarshal(csrData, &csr); err != nil || csr.CSR == "" {
		return "", fmt.Errorf("openbao: intermediate CSR missing")
	}
	// 2. Sign the CSR under THIS mount's root.
	signData, err := c.doPath(ctx, http.MethodPost, c.mount+"/root/sign-intermediate",
		map[string]string{"csr": csr.CSR, "format": "pem_bundle", "ttl": ttl})
	if err != nil {
		return "", fmt.Errorf("openbao: sign intermediate under root: %w", err)
	}
	var signed struct {
		Certificate  string `json:"certificate"`
		SerialNumber string `json:"serial_number"`
	}
	if err := json.Unmarshal(signData, &signed); err != nil || signed.Certificate == "" {
		return "", fmt.Errorf("openbao: signed intermediate missing")
	}
	// 3. Set the signed cert back on the intermediate mount (now an issuing CA).
	if _, err := c.doPath(ctx, http.MethodPost, intMount+"/intermediate/set-signed",
		map[string]string{"certificate": signed.Certificate}); err != nil {
		return "", fmt.Errorf("openbao: set-signed intermediate: %w", err)
	}
	return signed.SerialNumber, nil
}
