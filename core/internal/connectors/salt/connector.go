// Package salt is the Salt (SaltStack) Connector (charter §2.2): a core-tier,
// in-tree Connector shipping TWO capabilities — a Syncer over minion grains
// (host facts) and an event-bus Emitter over the Salt event stream.
//
// Transport: salt-api (the rest_cherrypy netapi). Auth is Salt external-auth
// (eauth): POST /login {username,password,eauth} returns an X-Auth-Token header
// carried on subsequent calls — plain HTTPS + a token, so the Go standard
// library suffices (no third-party Salt SDK). This is a third distinct auth
// model across the config-mgmt track (Chef Mixlib-RSA, Puppet mTLS, Salt token)
// — the abstraction generalizes, not a vendor lib (ADR-0039).
//
// Projection path (§1.2): grain observations flow through this package's
// Normalizer into the graph.Projector's normalizer write path — nothing here
// writes the graph any other way. The Salt master stays the authoritative SoR;
// the graph is a rebuildable read-model. Not a writable CMDB.
//
// Salt status (ADR-0039): Apache-2.0, Broadcom-maintained, 3008 LTS (2026) —
// live, single-vendor; the read-only HTTP boundary + permissive license hedge
// the strategic risk.
package salt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// Config locates a salt-api Source, shared by the Syncer and the Emitter.
// Credentials arrive from process env at startup (the vcenter/chef/puppet
// posture; CredentialRef brokering for Syncers is the recorded ADR-0009
// follow-up) — used only to mint eauth tokens, never persisted (§2.5).
type Config struct {
	// APIURL is the salt-api base, e.g. https://salt-master:8000
	APIURL string
	// Username / Password / Eauth are the external-auth credentials; Eauth
	// defaults to "pam".
	Username string
	Password string
	Eauth    string
	// SourceName names the registered Source and scopes the Syncer's writer id.
	SourceName string
	// EmitterName is the emitter name the event-bus Emitter publishes under;
	// Triggers reference it by this string.
	EmitterName string
	// EventTags, when non-empty, restricts forwarded Salt event tags to those
	// with one of these prefixes (empty = forward all; narrow it to avoid
	// flooding the emitter stream — ADR-0039).
	EventTags []string
}

// SyncerRef is the Syncer's writer identity for Provenance and the
// facet-ownership registry.
func (c Config) SyncerRef() string {
	return "connector/salt/" + c.SourceName + "/syncer"
}

// FacetNamespaces are the Facet namespaces the Syncer owns (§2.1 — one declared
// writer). SOURCE-scoped (salt.node.*, mirroring chef.node.*/puppet.node.*):
// two config-mgmt Syncers cannot share a namespace (one-owner registry), and a
// shared namespace would be last-writer-wins across Sources (§2.4). Cross-source
// hosts unify via dns.fqdn instead (ADR-0038). Curated charter-down from grains;
// uncovered by a pinned schema until a Contract demands one (§1.1).
func (c Config) FacetNamespaces() []types.FacetOwner {
	owner := func(ns string) types.FacetOwner {
		return types.FacetOwner{Namespace: ns, OwnerKind: "syncer", OwnerRef: c.SyncerRef()}
	}
	return []types.FacetOwner{
		owner("salt.node.identity"), // os, os_family, osfinger, osrelease, machine_id, saltversion
		owner("salt.node.os"),       // kernel, kernelrelease, kernelversion, cpuarch
		owner("salt.node.network"),  // ipv4, ipv6, fqdn_ip4
	}
}

func (c Config) eauth() string {
	if c.Eauth == "" {
		return "pam"
	}
	return c.Eauth
}

// saltClient is a stdlib salt-api client that holds an eauth token and
// re-logs-in on 401. Safe for one goroutine (Syncer or Emitter each build
// their own).
type saltClient struct {
	cfg   Config
	http  *http.Client
	mu    sync.Mutex
	token string
}

func newSaltClient(cfg Config) *saltClient {
	return &saltClient{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
}

// login mints a fresh eauth token from POST /login and stores it.
func (c *saltClient) login(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{
		"username": c.cfg.Username,
		"password": c.cfg.Password,
		"eauth":    c.cfg.eauth(),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.APIURL+"/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("salt: login: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("salt: login: %s", res.Status)
	}
	var out struct {
		Return []struct {
			Token string `json:"token"`
		} `json:"return"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return fmt.Errorf("salt: decode login: %w", err)
	}
	token := res.Header.Get("X-Auth-Token")
	if token == "" && len(out.Return) > 0 {
		token = out.Return[0].Token
	}
	if token == "" {
		return fmt.Errorf("salt: login returned no token")
	}
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
	return nil
}

// authToken returns the current token, logging in first if none is held.
func (c *saltClient) authToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	t := c.token
	c.mu.Unlock()
	if t != "" {
		return t, nil
	}
	if err := c.login(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token, nil
}

// str reads a string field from a decoded JSON map, "" when absent or non-string.
// Shared by the Emitter (the Syncer normalizer that also used it moved to the
// plugin, ADR-0046/0047 cutover).
func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
