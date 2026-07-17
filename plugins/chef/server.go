package chef

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	chefapi "github.com/go-chef/chef"
	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Config locates a Chef Infra Server Source. Credentials arrive resolved from
// the plugin's OWN broker at spawn (§2.5); the PEM key is used only to sign
// requests and never crosses the core.
type Config struct {
	// PluginID is the authenticated channel identity the operator grant is keyed on.
	PluginID string
	// ServerURL is the org-scoped Chef Infra Server base URL, terminated with a
	// slash per Chef convention, e.g. https://chef.example.com/organizations/acme/
	ServerURL string
	// ClientName is the Chef API client (user) id signing requests.
	ClientName string
	// KeyPEM is the plaintext RSA private key (PEM) for that client; sign-only,
	// never stored.
	KeyPEM string
	// AuthVersion selects the Mixlib sign protocol ("1.0" default, "1.3"
	// available). Legacy Chef servers (e.g. Chef 15) negotiate 1.0.
	AuthVersion string
	// SkipSSL disables TLS verification, for self-signed legacy Chef servers.
	SkipSSL bool
}

// Server implements the sovereign plugin port for a Syncer-class Chef plugin.
// It advertises the facet namespaces + tombstone schemes it REQUESTS to own; the
// core-side host honors them only where the operator grant allows.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg Config
	log *slog.Logger
}

func NewServer(cfg Config, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "chef"
	}
	return &Server{cfg: cfg, log: log.With("plugin", "chef")}
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: "v1",
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:           []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE},
		Contracts: []*pluginv1.ContractDecl{
			{SchemaId: "chef.node.identity"},
			{SchemaId: "chef.node.os"},
			{SchemaId: "chef.node.network"},
		},
		TombstoneSchemes: []string{"chef.node.name"},
	}}, nil
}

// Observe performs a full sync: Chef has no change feed, so it enumerates every
// node and streams them as ObservedEntities with the full_sync_complete boundary
// so the host can tombstone the chef.node.name values this Source no longer
// reports (ADR-0042) — never silent data loss.
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	ctx := stream.Context()
	client, err := connect(s.cfg)
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

func (c Config) authVersion() chefapi.AuthVersion {
	if c.AuthVersion == "1.3" {
		return chefapi.AuthVersion13
	}
	return chefapi.AuthVersion10
}

// connect builds the signing Chef API client. The library installs an
// RSA-signing round-tripper; we do not hold or reimplement the Mixlib crypto.
func connect(cfg Config) (*chefapi.Client, error) {
	client, err := chefapi.NewClient(&chefapi.Config{
		Name:                  cfg.ClientName,
		Key:                   cfg.KeyPEM,
		BaseURL:               cfg.ServerURL,
		SkipSSL:               cfg.SkipSSL,
		AuthenticationVersion: cfg.authVersion(),
	})
	if err != nil {
		return nil, fmt.Errorf("chef: build client for %q: %w", cfg.ServerURL, err)
	}
	return client, nil
}

// enumerate lists every node name, fetches and normalizes each, and returns the
// full set. A single node that fails to fetch/normalize is skipped, not fatal —
// one bad node never blocks the estate (§1.8). Pure content-expertise; no graph
// writes (the plugin holds no DB path).
func enumerate(ctx context.Context, client *chefapi.Client) ([]*pluginv1.ObservedEntity, error) {
	list, err := client.Nodes.List()
	if err != nil {
		return nil, fmt.Errorf("chef: list nodes: %w", err)
	}
	names := make([]string, 0, len(list))
	for name := range list {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic order for logs/tests

	out := make([]*pluginv1.ObservedEntity, 0, len(names))
	for _, name := range names {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		node, err := client.Nodes.Get(name)
		if err != nil {
			continue
		}
		e, err := normalizeNode(node)
		if err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}
