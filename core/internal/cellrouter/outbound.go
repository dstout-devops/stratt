package cellrouter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PeerClient makes authenticated outbound control calls to a peer Cell from
// OUTSIDE the middleware — specifically a Temporal activity launching and
// polling a cross-Cell child Run (ADR-0044 slice 5). It is the write-side
// counterpart of the router's inbound read federation: every call is HMAC-signed
// (body-covered for writes) and marked as a fan-out so the peer serves it
// local-only and never re-federates.
//
// Identity travels as an ASSERTION (X-Stratt-Principal), not a bearer: the async
// launch has no live token to forward (§2.5, tokens never persist). The peer
// trusts the assertion only because the HMAC proves it came from a Cell holding
// the shared secret, and re-evaluates authz locally against the global OpenFGA
// (§1.6). Empty Secret ⇒ single-Cell: PeerClient is never invoked (no peers).
type PeerClient struct {
	HTTP   *http.Client
	Secret []byte
}

// NewPeerClient builds a PeerClient with a bounded default timeout.
func NewPeerClient(secret []byte) *PeerClient {
	return &PeerClient{HTTP: &http.Client{Timeout: 30 * time.Second}, Secret: secret}
}

// principalHeaders carries the asserted acting identity (id + kind) — the
// trusted-assertion the peer honors on a verified fan-out.
func principalHeaders(principalID, principalKind string) map[string]string {
	h := map[string]string{}
	if principalID != "" {
		h["X-Stratt-Principal"] = principalID
		if principalKind != "" {
			h["X-Stratt-Principal-Kind"] = principalKind
		}
	}
	return h
}

// Post performs one HMAC-signed (body-covered) POST to a peer's /api/v1 path,
// asserting the acting Principal. Returns the peer's status and body.
func (c *PeerClient) Post(ctx context.Context, endpoint, path string, body []byte, principalID, principalKind string) (int, []byte, error) {
	return c.do(ctx, http.MethodPost, endpoint, path, "", body, principalID, principalKind)
}

// Get performs one HMAC-signed GET to a peer's /api/v1 path (child-Run status
// polling), asserting the acting Principal so a grant-guarded read resolves.
func (c *PeerClient) Get(ctx context.Context, endpoint, path, rawQuery, principalID, principalKind string) (int, []byte, error) {
	return c.do(ctx, http.MethodGet, endpoint, path, rawQuery, nil, principalID, principalKind)
}

func (c *PeerClient) do(ctx context.Context, method, endpoint, path, rawQuery string, body []byte, principalID, principalKind string) (int, []byte, error) {
	url := strings.TrimRight(endpoint, "/") + "/api/v1" + path
	if rawQuery != "" {
		url += "?" + rawQuery
	}
	var rdr io.Reader
	if len(body) > 0 {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range principalHeaders(principalID, principalKind) {
		req.Header.Set(k, v)
	}
	req.Header.Set(fanoutHeader, "1")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if len(c.Secret) > 0 {
		// Bind the asserted Principal into the signature so a replay cannot
		// rewrite the identity the peer will authorize the write under.
		req.Header.Set(authHeader, signCellAuth(c.Secret, method, path, rawQuery, hashBody(body),
			principalID, principalKind, time.Now().Unix()))
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("cellrouter: read peer body: %w", err)
	}
	return resp.StatusCode, respBody, nil
}
