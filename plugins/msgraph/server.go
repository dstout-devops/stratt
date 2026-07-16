package msgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/oauth2/clientcredentials"
	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// errResync marks an expired delta token (HTTP 410): the host's stored cursor is
// worthless, so this Observe degrades to a clean full enumeration in-plugin and
// re-emits the full-sync boundary (never silent data loss).
var errResync = errors.New("msgraph: delta token expired; full resync required")

// defaultEndpoint is the Graph base URL including version (overridable for the
// dev sim).
const defaultEndpoint = "https://graph.microsoft.com/v1.0"

// Config locates the Graph Source. Credentials arrive resolved from the plugin's
// OWN broker at spawn (§2.5); material never crosses the core and is used only to
// mint bearer tokens, never persisted.
type Config struct {
	PluginID string // the authenticated channel identity the operator grant is keyed on
	// Endpoint is the Graph base URL including version, default
	// https://graph.microsoft.com/v1.0.
	Endpoint string
	TenantID string
	ClientID string
	// ClientSecret is used only to mint tokens; never stored.
	ClientSecret string
	// TokenURL overrides the tenant-derived token endpoint (dev sim).
	TokenURL string
}

func (c Config) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	return defaultEndpoint
}

// deltaURL is the initial full-enumeration entrypoint (no $deltatoken).
func (c Config) deltaURL() string {
	return strings.TrimRight(c.endpoint(), "/") + "/devices/delta"
}

func (c Config) tokenURL() string {
	if c.TokenURL != "" {
		return c.TokenURL
	}
	return "https://login.microsoftonline.com/" + c.TenantID + "/oauth2/v2.0/token"
}

// scope derives the .default scope from the endpoint origin, so the sim and the
// real service both get a coherent value.
func (c Config) scope() string {
	origin := c.endpoint()
	if i := strings.Index(origin, "/v1.0"); i > 0 {
		origin = origin[:i]
	}
	return origin + "/.default"
}

// httpClient returns an OAuth2 client-credentials transport that mints and
// refreshes bearer tokens as needed. Auth is OAuth2 client credentials → bearer.
func (c Config) httpClient(ctx context.Context) *http.Client {
	cc := clientcredentials.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		TokenURL:     c.tokenURL(),
		Scopes:       []string{c.scope()},
	}
	return cc.Client(ctx)
}

// Server implements the sovereign plugin port for a Syncer-class Graph plugin.
// It advertises the facet namespaces + tombstone scheme it REQUESTS to own; the
// core-side host honors them only where the operator grant allows.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg Config
	log *slog.Logger
}

func NewServer(cfg Config, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "msgraph"
	}
	return &Server{cfg: cfg, log: log.With("plugin", "msgraph")}
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: "v1",
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:           []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE},
		Contracts: []*pluginv1.ContractDecl{
			{SchemaId: "device.identity"}, // azureAdDeviceId, trustType, profileType
			{SchemaId: "device.os"},       // operatingSystem, version
			{SchemaId: "device.state"},    // accountEnabled, approximate last sign-in
		},
		// Tombstone on the plugin's OWN identity scheme (the Entra directory
		// object id), which no other Source emits — absent devices are
		// graph-scoped removals.
		TombstoneSchemes: []string{"graph.id"},
		// Graph delta is poll-based, not a held stream like vSphere's
		// PropertyCollector — the host polls Observe on a cadence with the cursor.
		ObserveMode: pluginv1.Manifest_OBSERVE_MODE_POLL,
	}}, nil
}

// Observe drives the DELTA-cursor path. An empty req.Cursor runs the INITIAL full
// enumeration (paged over @odata.nextLink) and emits FullSyncComplete=true with
// the terminal @odata.deltaLink as NextCursor, so the host tombstones every
// device absent from the seen-set (ADR-0042). A non-empty cursor resumes from
// that deltaLink and emits only the changed devices plus a Gone entry per
// @removed (by the graph.id tombstone scheme), FullSyncComplete=false, carrying
// the new deltaLink. An expired token (HTTP 410) degrades in-plugin to one clean
// full pass. The plugin holds NO graph write path — the HOST persists the cursor.
func (s *Server) Observe(req *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	ctx := stream.Context()
	client := s.cfg.httpClient(ctx)

	cursor := req.GetCursor()
	initial := cursor == ""
	start := cursor
	if initial {
		start = s.cfg.deltaURL()
	}

	entities, gone, deltaLink, err := s.walk(ctx, client, start)
	if errors.Is(err, errResync) {
		// Expired delta token: the cursor the host handed us is worthless. Run a
		// clean full enumeration and re-emit the full-sync boundary.
		s.log.Warn("delta token expired; running full resync")
		entities, gone, deltaLink, err = s.walk(ctx, client, s.cfg.deltaURL())
		if err != nil {
			return err
		}
		s.log.Info("full resync complete", "devices", len(entities))
		return stream.Send(&pluginv1.ObserveResponse{
			Entities: entities, FullSyncComplete: true, NextCursor: deltaLink,
		})
	}
	if err != nil {
		return err
	}

	if initial {
		s.log.Info("full sync complete", "devices", len(entities))
	} else {
		s.log.Info("delta window", "changed", len(entities), "gone", len(gone))
	}
	return stream.Send(&pluginv1.ObserveResponse{
		Entities:         entities,
		Gone:             gone,
		FullSyncComplete: initial,
		NextCursor:       deltaLink,
	})
}

// walk follows @odata.nextLink from start, accumulating normalized ObservedEntities
// (live devices) and GoneEntity tombstones (@removed devices, by the graph.id
// scheme), until it reaches the terminal @odata.deltaLink — which it returns as
// the next cursor. A device that fails to normalize is skipped, not fatal (§1.8).
func (s *Server) walk(ctx context.Context, client *http.Client, start string) ([]*pluginv1.ObservedEntity, []*pluginv1.GoneEntity, string, error) {
	var (
		entities  []*pluginv1.ObservedEntity
		gone      []*pluginv1.GoneEntity
		deltaLink string
	)
	url := start
	for url != "" {
		if ctx.Err() != nil {
			return nil, nil, "", ctx.Err()
		}
		page, err := getPage(ctx, client, url)
		if err != nil {
			return nil, nil, "", err
		}
		for _, d := range page.Value {
			if d.Removed != nil {
				gone = append(gone, &pluginv1.GoneEntity{Scheme: "graph.id", Value: d.ID})
				continue
			}
			e, err := normalizeDevice(d)
			if err != nil {
				s.log.Warn("skipping device", "error", err)
				continue
			}
			entities = append(entities, e)
		}
		switch {
		case page.NextLink != "":
			url = page.NextLink
		case page.DeltaLink != "":
			deltaLink = page.DeltaLink
			url = ""
		default:
			return nil, nil, "", fmt.Errorf("msgraph: delta page carried neither nextLink nor deltaLink")
		}
	}
	return entities, gone, deltaLink, nil
}

// getPage fetches one delta page. HTTP 410 (syncStateNotFound) surfaces as
// errResync so Observe can fall back to a full pass.
func getPage(ctx context.Context, client *http.Client, url string) (deltaPage, error) {
	var page deltaPage
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return page, err
	}
	res, err := client.Do(req)
	if err != nil {
		return page, fmt.Errorf("msgraph: delta request: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode == http.StatusGone {
		return page, errResync
	}
	if res.StatusCode != http.StatusOK {
		return page, fmt.Errorf("msgraph: delta request: %s", res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		return page, fmt.Errorf("msgraph: decode delta page: %w", err)
	}
	return page, nil
}
