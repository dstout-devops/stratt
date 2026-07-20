package awx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config locates the AWX/AAP Controller this Connector reads.
type Config struct {
	// Endpoint is the Controller base URL, e.g. https://awx.example.com (no /api/v2).
	Endpoint string
	// Token is an AWX OAuth2 application/personal token, sent as a bearer. Read-only
	// scope is sufficient — this Connector never writes back to AWX (§1.2).
	Token      string
	HTTPClient *http.Client
	// ControllerID qualifies every projected identity so two Controllers' identically
	// numbered objects never collide in one estate (mirrors kubecontainers' cluster
	// qualifier). "" ⇒ the Endpoint host.
	ControllerID string
}

// Client is a minimal read-only AWX /api/v2 client. It is the plugin's own SoR
// integration (module isolation, ADR-0046) — the core importer keeps its own client.
type Client struct {
	base   string
	token  string
	http   *http.Client
	ctrlID string
}

// New builds a read client. The base is normalized to <endpoint>/api/v2.
func New(cfg Config) *Client {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	base := strings.TrimRight(cfg.Endpoint, "/") + "/api/v2"
	ctrl := cfg.ControllerID
	if ctrl == "" {
		if u, err := url.Parse(cfg.Endpoint); err == nil && u.Host != "" {
			ctrl = u.Host
		} else {
			ctrl = cfg.Endpoint
		}
	}
	return &Client{base: base, token: cfg.Token, http: hc, ctrlID: ctrl}
}

// ControllerID is the qualifier prefixed onto every projected identity.
func (c *Client) ControllerID() string { return c.ctrlID }

// page is the AWX paginated envelope: results + a next-page cursor.
type page[T any] struct {
	Next    string `json:"next"`
	Results []T    `json:"results"`
}

// list walks every page of an AWX collection endpoint (path is relative to
// /api/v2, e.g. "/job_templates/") and returns the flattened results.
func list[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var out []T
	next := c.base + path
	for next != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("awx: GET %s: %w", path, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("awx: GET %s: status %d: %s", path, resp.StatusCode, truncate(body))
		}
		var p page[T]
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, fmt.Errorf("awx: decode %s: %w", path, err)
		}
		out = append(out, p.Results...)
		// AWX `next` is a relative /api/v2/... path (or null at the end).
		if p.Next == "" {
			break
		}
		next = originOf(c.base) + p.Next
	}
	return out, nil
}

func truncate(b []byte) string {
	if len(b) > 200 {
		return string(b[:200])
	}
	return string(b)
}

// originOf returns scheme://host for a base like https://host/api/v2, so a
// relative `next` cursor can be re-absolutized.
func originOf(base string) string {
	if u, err := url.Parse(base); err == nil {
		return u.Scheme + "://" + u.Host
	}
	return ""
}
