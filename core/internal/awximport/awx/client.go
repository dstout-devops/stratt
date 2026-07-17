package awx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Config locates the AWX instance and its API token. Credentials are supplied
// at invocation (CLI flag / env) — never persisted (§2.5). This is a one-shot
// read client, so there is no Source registration or cursor.
type Config struct {
	// Endpoint is the AWX base URL, e.g. https://awx.example.com (no /api/v2).
	Endpoint string
	// Token is an AWX OAuth2 application/personal token, sent as a bearer.
	Token string
	// HTTPClient overrides the default (tests inject the awxsim server client).
	HTTPClient *http.Client
}

// Client reads an AWX /api/v2 surface. Zero business logic: it enumerates and
// decodes; the transform lives in core/internal/awximport.
type Client struct {
	cfg    Config
	http   *http.Client
	apiURL string
}

// New returns a read client for cfg.
func New(cfg Config) *Client {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{
		cfg:    cfg,
		http:   hc,
		apiURL: strings.TrimRight(cfg.Endpoint, "/") + "/api/v2",
	}
}

// get decodes one AWX object at an absolute or /api/v2-relative path.
func get[T any](ctx context.Context, c *Client, path string) (T, error) {
	var out T
	u := c.resolve(path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return out, err
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return out, fmt.Errorf("awx: GET %s: %w", u, err)
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return out, fmt.Errorf("awx: GET %s: %s (check --token)", u, res.Status)
	}
	if res.StatusCode != http.StatusOK {
		return out, fmt.Errorf("awx: GET %s: %s", u, res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("awx: decode %s: %w", u, err)
	}
	return out, nil
}

// list enumerates a paginated collection, following the envelope's next link
// until exhausted. relPath is /api/v2-relative (e.g. "/job_templates/").
func list[T any](ctx context.Context, c *Client, relPath string) ([]T, error) {
	var all []T
	next := relPath
	for next != "" {
		p, err := get[page[T]](ctx, c, next)
		if err != nil {
			return nil, err
		}
		all = append(all, p.Results...)
		next = p.Next
	}
	return all, nil
}

// resolve turns an AWX path into an absolute URL. AWX next-links are usually
// root-relative ("/api/v2/...?page=2"); those join onto the endpoint origin. A
// bare "/collection/" joins onto the /api/v2 base.
func (c *Client) resolve(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if strings.HasPrefix(path, "/api/") {
		if u, err := url.Parse(c.cfg.Endpoint); err == nil {
			return u.Scheme + "://" + u.Host + path
		}
		return strings.TrimRight(c.cfg.Endpoint, "/") + path
	}
	return c.apiURL + path
}
