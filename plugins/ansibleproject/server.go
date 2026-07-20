package ansibleproject

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// ServerConfig is the ansible-project Syncer's projection tuning.
type ServerConfig struct {
	PluginID string // the authenticated channel identity the operator grant is keyed on

	// AllowEmptyFullSync governs the empty-snapshot guardrail (§1.8), mirroring the AWX
	// Connector: every Observe is a full sync driving the per-source tombstone sweep, so
	// an EMPTY read — far more often a missing/unmounted content root or a bad path than a
	// genuinely empty repo — would otherwise retract EVERY projected artifact and the
	// mirror would go silently blank. By default (false) an empty snapshot holds steady
	// and logs loudly, self-healing on the next non-empty read.
	AllowEmptyFullSync bool
}

// Server implements the sovereign plugin port for the ansible-project Syncer: it
// OBSERVEs a raw Ansible content root and projects its artifacts as `ansible.*`
// ObservedEntities. Read-only (§1.2): Git stays the system-of-record; the plugin holds
// no graph write path — it proposes typed values, the core-side host governs writes
// (namespace ownership, identity-scheme gating, per-source tombstone).
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg    ServerConfig
	client *Client
	log    *slog.Logger
}

func NewServer(cfg ServerConfig, client *Client, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "ansibleproject"
	}
	return &Server{cfg: cfg, client: client, log: log.With("plugin", "ansibleproject")}
}

// GetManifest advertises the SYNCER class + the four `ansible.*` Facet namespaces this
// Syncer owns — and ONLY those it actually populates (§1.1: own what you project). The
// identity + relation schemes it emits are gated by the operator grant (strattd side),
// never self-granted.
func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	kinds := []string{KindPlaybook, KindRole, KindCollection, KindInventory}
	contracts := make([]*pluginv1.ContractDecl, 0, len(kinds))
	for _, k := range kinds {
		contracts = append(contracts, &pluginv1.ContractDecl{SchemaId: k})
	}
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: "v1",
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:           []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE},
		ObserveMode:     pluginv1.Manifest_OBSERVE_MODE_POLL,
		Contracts:       contracts,
		// A removed artifact retracts on the full-sync boundary, per object-type scheme —
		// and ONLY this Syncer's own schemes, so a co-projecting Source (AWX) can never be
		// cross-tombstoned (per-source liveness, ADR-0042).
		TombstoneSchemes: kinds,
	}}, nil
}

// Observe performs one full read of the content root and streams the projected
// `ansible.*` entities with the full_sync_complete boundary (so the host tombstones
// artifacts since removed from Git). The content root has no change feed, so every
// cycle is a full enumeration; the empty-snapshot guardrail (§1.8) keeps a missing/
// unmounted root from masquerading as an emptied repo.
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	snap, err := s.client.Enumerate()
	if err != nil {
		return err
	}
	entities, err := s.client.Normalize(snap)
	if err != nil {
		return err
	}

	if len(entities) == 0 && !s.cfg.AllowEmptyFullSync {
		s.log.Warn("empty content-root read — declining to assert a full sync (likely a missing/unmounted root or bad path); " +
			"holding the existing mirror, will reconcile on the next non-empty read. Set AllowEmptyFullSync for a root expected to be empty.")
		return stream.Send(&pluginv1.ObserveResponse{FullSyncComplete: false})
	}

	s.log.Info("full sync", "project", s.client.ProjectID(),
		"playbooks", len(snap.Playbooks), "roles", len(snap.Roles),
		"collections", len(snap.Collections), "inventories", len(snap.Inventories))
	return stream.Send(&pluginv1.ObserveResponse{Entities: entities, FullSyncComplete: true})
}
