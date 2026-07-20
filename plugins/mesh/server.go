package mesh

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// TrafficSource is the seam between this plugin's content-expertise and the concrete
// telemetry backend. An implementation returns a FULL snapshot of the currently-
// observed caller→callee edges. Keeping it an interface is the "nothing is baked in"
// discipline (§1.4): the Prometheus implementation below is one flavor; a Kiali-graph
// reader, a Hubble/Cilium reader, or a Consul reader would be another — none of them is
// core, none of them is load-bearing for the deterministic spine.
type TrafficSource interface {
	// Edges returns the current full set of observed service dependencies. It is an
	// instant snapshot, not a delta — every Observe is a full sync (see Observe).
	Edges(ctx context.Context) ([]TrafficEdge, error)
}

// Config locates the plugin identity.
type Config struct {
	PluginID string // the authenticated channel identity the operator grant is keyed on

	// AllowEmptyFullSync governs the empty-snapshot guardrail (§1.8 — never present a
	// config error as valid data). Because every Observe is a full sync that drives the
	// host's relation-presence GC, an EMPTY result vector — far more often a misconfigured
	// query/labels against a live mesh than a genuinely idle one — would otherwise emit a
	// confident full-sync boundary and silently retract EVERY mesh-asserted `depends-on`
	// edge. By default (false) an empty snapshot is treated as suspect: the cycle holds
	// steady (no full-sync boundary, so no GC) and logs loudly, self-healing on the next
	// non-empty read. Set true only for a mesh legitimately expected to reach zero edges.
	AllowEmptyFullSync bool
}

// Server implements the sovereign plugin port for the mesh dependency Syncer
// (ADR-0082 slice 2). It OBSERVEs a service-mesh's request telemetry and projects the
// runtime `depends-on` edges of the service dimension. The plugin holds no graph write
// path (§1.2): it proposes typed ObservedEntity/ObservedRelation values; the core-side
// host governs writes and does the per-source relation-presence full-sync GC that makes
// a multi-source dependency (mesh + declared) correct (ADR-0082).
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg Config
	src TrafficSource
	log *slog.Logger
}

// NewServer wires the plugin over a TrafficSource (the telemetry transport).
func NewServer(cfg Config, src TrafficSource, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "mesh"
	}
	return &Server{cfg: cfg, src: src, log: log.With("plugin", "mesh")}
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: "v1",
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:           []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE},
		// No Contracts: the mesh emits NO Facet — it anchors service identity and
		// asserts identity-only `depends-on` edges (§1.1 — no schema without a demanded
		// Contract; §2.1 — kubeservices owns service.endpoint, not the mesh).
		//
		// Tombstone on the shared `dns.fqdn` scheme: a service the mesh no longer sees
		// any traffic to/from has its mesh presence retracted. Union liveness (ADR-0042)
		// keeps it alive if kubeservices still observes it; a mesh-ONLY service (a true
		// external dependency) is collected when its traffic stops. Its `depends-on`
		// edges are collected by the host's per-source relation-presence replace
		// (ADR-0082) and the endpoint-tombstone cascade.
		TombstoneSchemes: []string{SchemeFQDN},
	}}, nil
}

// Observe performs a full sync: read the current dependency snapshot from the mesh
// telemetry, normalize it, and emit the service anchors + `depends-on` edges with the
// full_sync_complete boundary. Mesh telemetry is a live gauge with no change feed here,
// so every cycle is a full enumeration; the host's relation-presence GC (ADR-0082)
// collects a dependency that has dropped out of the snapshot (its last asserter gone).
// An empty snapshot is guarded (see Config.AllowEmptyFullSync) so a misconfiguration
// cannot masquerade as a legitimate no-dependencies full sync and mass-retract.
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	edges, err := s.src.Edges(stream.Context())
	if err != nil {
		return err
	}
	entities := Normalize(edges)

	// Empty-snapshot guardrail (§1.8): decline to assert a full sync over zero edges
	// unless explicitly permitted — emitting FullSyncComplete here would retract every
	// mesh-asserted dependency on what is most likely a misconfiguration. Hold steady
	// (no boundary ⇒ the host runs no GC) and surface it loudly. Union liveness still
	// protects the service entities via any co-asserting Source (ADR-0042).
	if len(entities) == 0 && !s.cfg.AllowEmptyFullSync {
		s.log.Warn("empty dependency snapshot — declining to assert a full sync (likely a query/label misconfiguration); "+
			"holding existing edges, will reconcile on the next non-empty read. Set AllowEmptyFullSync for a mesh expected to be idle.",
			"edges", len(edges))
		return stream.Send(&pluginv1.ObserveResponse{FullSyncComplete: false})
	}

	s.log.Info("full sync", "edges", len(edges), "anchors", len(entities))
	return stream.Send(&pluginv1.ObserveResponse{Entities: entities, FullSyncComplete: true})
}
