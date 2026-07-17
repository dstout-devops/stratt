package puppet

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// defaultPageLimit is the /inventory page size — one query returns a node with
// its full fact set, so pages are cheap; 500 balances round-trips vs memory.
const defaultPageLimit = 500

// Config locates a PuppetDB-compatible Source. Client-certificate material
// arrives resolved from the plugin's OWN broker at spawn (§2.5); material never
// crosses the core. It is used only to establish mTLS and is never persisted.
type Config struct {
	// PluginID is the authenticated channel identity the operator grant is keyed on.
	PluginID string
	// BaseURL is the PuppetDB/OpenVoxDB base, e.g. https://puppetdb:8081 (mTLS)
	// or http://localhost:8080 (dev, no cert).
	BaseURL string
	// CertFile / KeyFile / CAFile are the mTLS client cert, key, and the Puppet
	// CA to trust. Required for an https:// BaseURL; ignored for http://.
	CertFile string
	KeyFile  string
	CAFile   string
}

// Server implements the sovereign plugin port for a Syncer-class puppet plugin.
// It advertises the facet namespaces + tombstone schemes it REQUESTS to own; the
// core-side host honors them only where the operator grant allows.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg       Config
	log       *slog.Logger
	pageLimit int
	client    *http.Client
}

func NewServer(cfg Config, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "puppet"
	}
	return &Server{cfg: cfg, log: log.With("plugin", "puppet"), pageLimit: defaultPageLimit}
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: "v1",
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:           []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE},
		Contracts: []*pluginv1.ContractDecl{
			{SchemaId: "puppet.node.identity"},
			{SchemaId: "puppet.node.os"},
			{SchemaId: "puppet.node.network"},
		},
		TombstoneSchemes: []string{"puppet.certname"},
	}}, nil
}

// Observe performs a full sync: PuppetDB has no change feed, so every cycle is a
// full paged enumeration of the /inventory endpoint. It emits the normalized
// nodes with the full_sync_complete boundary so the host can tombstone every
// puppet.certname this Source no longer reports (ADR-0042) — never silent data
// loss. A single node that fails to normalize is skipped, not fatal (§1.8).
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	ctx := stream.Context()
	if s.client == nil {
		client, err := connect(s.cfg)
		if err != nil {
			return err
		}
		s.client = client
	}

	entities, err := s.enumerate(ctx)
	if err != nil {
		return err
	}
	s.log.Info("full sync", "entities", len(entities))
	return stream.Send(&pluginv1.ObserveResponse{Entities: entities, FullSyncComplete: true})
}

// enumerate pages the /inventory endpoint and normalizes each node. Pure content-
// expertise; no graph writes (the plugin holds no DB path).
func (s *Server) enumerate(ctx context.Context) ([]*pluginv1.ObservedEntity, error) {
	var out []*pluginv1.ObservedEntity
	offset := 0
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		entries, err := s.fetchInventory(ctx, offset)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			ent, err := normalizeNode(e)
			if err != nil {
				s.log.Warn("skipping node", "certname", e.Certname, "error", err)
				continue
			}
			out = append(out, ent)
		}
		offset += len(entries)
		if len(entries) < s.pageLimit {
			break // short page = last page
		}
	}
	return out, nil
}

// fetchInventory GETs one page of /pdb/query/v4/inventory ordered by certname
// for stable paging (PuppetDB does not guarantee order without order_by).
func (s *Server) fetchInventory(ctx context.Context, offset int) ([]inventoryEntry, error) {
	q := url.Values{}
	q.Set("order_by", `[{"field":"certname"}]`)
	q.Set("limit", strconv.Itoa(s.pageLimit))
	q.Set("offset", strconv.Itoa(offset))
	if offset == 0 {
		q.Set("include_total", "true")
	}
	endpoint := s.cfg.BaseURL + "/pdb/query/v4/inventory?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("puppet: inventory request: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("puppet: inventory request: %s", res.Status)
	}
	var entries []inventoryEntry
	if err := json.NewDecoder(res.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("puppet: decode inventory: %w", err)
	}
	return entries, nil
}

// connect builds a stdlib client. For an https:// BaseURL it configures mTLS
// (client cert + the Puppet CA trust pool); for http:// (localhost dev) it
// returns a plain client. No third-party dependency (§1.4).
func connect(cfg Config) (*http.Client, error) {
	if strings.HasPrefix(cfg.BaseURL, "http://") {
		return &http.Client{Timeout: 30 * time.Second}, nil
	}
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("puppet: load client cert: %w", err)
	}
	pool := x509.NewCertPool()
	caPEM, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("puppet: read CA %q: %w", cfg.CAFile, err)
	}
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("puppet: CA file %q held no valid certificates", cfg.CAFile)
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{cert},
				RootCAs:      pool,
				MinVersion:   tls.VersionTLS12,
			},
		},
	}, nil
}
