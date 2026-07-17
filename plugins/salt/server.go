package salt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Config locates a salt-api Source. Credentials arrive resolved from the
// plugin's OWN broker at spawn (§2.5); material never crosses the core, and is
// used only to mint eauth tokens, never persisted.
type Config struct {
	PluginID string // the authenticated channel identity the operator grant is keyed on
	// APIURL is the salt-api base, e.g. https://salt-master:8000
	APIURL string
	// Username / Password / Eauth are the external-auth credentials; Eauth
	// defaults to "pam".
	Username string
	Password string
	Eauth    string
	// EventTags is the Emitter's tag-prefix allowlist (empty = forward all).
	EventTags []string
}

func (c Config) eauth() string {
	if c.Eauth == "" {
		return "pam"
	}
	return c.Eauth
}

// Server implements the sovereign plugin port for a Syncer-class Salt plugin.
// It advertises the facet namespaces + tombstone schemes it REQUESTS to own; the
// core-side host honors them only where the operator grant allows.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg Config
	log *slog.Logger
}

func NewServer(cfg Config, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "salt"
	}
	return &Server{cfg: cfg, log: log.With("plugin", "salt")}
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: "v1",
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:           []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE, pluginv1.Verb_VERB_EMIT},
		Contracts: []*pluginv1.ContractDecl{
			{SchemaId: "salt.node.identity"},
			{SchemaId: "salt.node.os"},
			{SchemaId: "salt.node.network"},
		},
		// Tombstone on the plugin's OWN identity scheme, not the shared dns.fqdn
		// (which other Sources also emit): absent minions are salt-scoped removals.
		TombstoneSchemes: []string{"salt.minion_id"},
	}}, nil
}

// Observe performs a full sync: the runner cache.grains enumeration (the
// master's grain cache — no minion round-trip, immune to dead minions) mapped to
// ObservedEntities with the full_sync_complete boundary so the host can
// tombstone the minions the master no longer reports (ADR-0042). Salt has no
// grain change feed, so every cycle is a full enumeration.
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	ctx := stream.Context()
	client, err := connect(ctx, s.cfg)
	if err != nil {
		return err
	}

	entities, err := enumerate(ctx, client)
	if err != nil {
		return err
	}
	s.log.Info("full sync", "entities", len(entities))
	return stream.Send(&pluginv1.ObserveResponse{Entities: entities, FullSyncComplete: true})
}

// connect builds a salt-api client and validates connectivity by minting an
// eauth token. The token is re-minted lazily on 401 during enumeration.
func connect(ctx context.Context, cfg Config) (*saltClient, error) {
	c := newSaltClient(cfg)
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// enumerate runs one full cache.grains enumeration and normalizes it. A single
// minion that fails to normalize is skipped, not fatal (§1.8). Pure
// content-expertise; no graph writes (the plugin holds no DB path).
func enumerate(ctx context.Context, c *saltClient) ([]*pluginv1.ObservedEntity, error) {
	grainsByMinion, err := c.cacheGrains(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(grainsByMinion))
	for id := range grainsByMinion {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic order for logs/tests

	out := make([]*pluginv1.ObservedEntity, 0, len(ids))
	for _, id := range ids {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		e, err := normalizeMinion(id, grainsByMinion[id])
		if err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// saltClient is a stdlib salt-api client that holds an eauth token and
// re-logs-in on 401. Auth is Salt external-auth (eauth): POST /login
// {username,password,eauth} returns an X-Auth-Token header carried on subsequent
// calls — plain HTTPS + a token, so the Go standard library suffices.
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

// cacheGrains calls the runner cache.grains and returns minion-id -> grains.
// tgt is mandatory since Salt 3001, so "*" is always sent.
func (c *saltClient) cacheGrains(ctx context.Context) (map[string]map[string]any, error) {
	token, err := c.authToken(ctx)
	if err != nil {
		return nil, err
	}
	lowstate, _ := json.Marshal(map[string]any{
		"client":   "runner",
		"fun":      "cache.grains",
		"tgt":      "*",
		"tgt_type": "glob",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.APIURL+"/", bytes.NewReader(lowstate))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Auth-Token", token)

	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("salt: cache.grains request: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode == http.StatusUnauthorized {
		// Token expired — drop it so the next cycle re-logs-in.
		c.mu.Lock()
		c.token = ""
		c.mu.Unlock()
		return nil, fmt.Errorf("salt: cache.grains: unauthorized (token cleared for re-login)")
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("salt: cache.grains: %s", res.Status)
	}
	var out struct {
		Return []map[string]map[string]any `json:"return"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("salt: decode cache.grains: %w", err)
	}
	if len(out.Return) == 0 {
		return map[string]map[string]any{}, nil
	}
	return out.Return[0], nil
}
