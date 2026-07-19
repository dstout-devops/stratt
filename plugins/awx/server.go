package awx

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// ServerConfig is the AWX Connector's projection tuning.
type ServerConfig struct {
	PluginID string // the authenticated channel identity the operator grant is keyed on

	// AllowEmptyFullSync governs the empty-snapshot guardrail (§1.8), mirroring the
	// kubecontainers/mesh collectors: every Observe is a full sync driving the per-source
	// tombstone sweep, so an EMPTY read — far more often a transient outage / auth / RBAC
	// issue than a Controller with genuinely zero content — would otherwise retract EVERY
	// ansible.* entity and the mirror would go silently blank. By default (false) an empty
	// snapshot holds steady and logs loudly, self-healing on the next non-empty read.
	AllowEmptyFullSync bool
}

// Server implements the sovereign plugin port for the AWX Connector: it OBSERVEs an
// AWX/AAP Controller's /api/v2 and projects its automation estate as `ansible.*`
// ObservedEntities. Read-only (§1.2): AWX stays the system-of-record and keeps
// executing; the plugin holds no graph write path — it proposes typed values, the
// core-side host governs writes (namespace ownership, identity/relation-scheme gating,
// per-source tombstone).
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg    ServerConfig
	client *Client
	log    *slog.Logger
}

func NewServer(cfg ServerConfig, client *Client, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "awx"
	}
	return &Server{cfg: cfg, client: client, log: log.With("plugin", "awx")}
}

// GetManifest advertises the SYNCER class + the `ansible.*` Facet namespaces this
// Connector owns. The identity + relation schemes it emits are gated by the operator
// grant (strattd side), never self-granted.
func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: "v1",
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:           []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE},
		ObserveMode:     pluginv1.Manifest_OBSERVE_MODE_POLL,
		Contracts: []*pluginv1.ContractDecl{
			{SchemaId: KindTemplate},
			{SchemaId: KindWorkflow},
			{SchemaId: KindSchedule},
			{SchemaId: KindOrg},
			{SchemaId: KindTeam},
		},
		// A removed AWX object retracts on the full-sync boundary, per object-type scheme.
		// Union liveness (ADR-0042) keeps an entity alive if another Source still asserts it.
		TombstoneSchemes: []string{KindTemplate, KindWorkflow, KindSchedule, KindOrg, KindTeam},
	}}, nil
}

// Observe performs one full read of the Controller and streams the projected
// `ansible.*` entities with the full_sync_complete boundary (so the host tombstones
// AWX objects that have since been deleted). AWX has no change feed here, so every
// cycle is a full enumeration; the empty-snapshot guardrail (§1.8) keeps a transient
// read failure from masquerading as an emptied Controller.
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	snap, err := s.client.Enumerate(stream.Context())
	if err != nil {
		return err
	}
	entities, err := s.client.Normalize(snap)
	if err != nil {
		return err
	}

	if len(entities) == 0 && !s.cfg.AllowEmptyFullSync {
		s.log.Warn("empty AWX read — declining to assert a full sync (likely a transient outage / auth / RBAC issue); " +
			"holding the existing mirror, will reconcile on the next non-empty read. Set AllowEmptyFullSync for a Controller expected to be empty.")
		return stream.Send(&pluginv1.ObserveResponse{FullSyncComplete: false})
	}

	s.log.Info("full sync", "controller", s.client.ControllerID(),
		"templates", len(snap.JobTemplates), "workflows", len(snap.Workflows),
		"schedules", len(snap.Schedules), "orgs", len(snap.Organizations), "teams", len(snap.Teams))
	return stream.Send(&pluginv1.ObserveResponse{Entities: entities, FullSyncComplete: true})
}
