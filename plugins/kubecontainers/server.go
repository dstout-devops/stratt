package kubecontainers

import (
	"context"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// ServerConfig locates the collection scope + projection tuning.
type ServerConfig struct {
	PluginID  string // the authenticated channel identity the operator grant is keyed on
	Namespace string // "" ⇒ all namespaces
	Config           // projection tuning (ClusterID, InternalRegistry)

	// AllowEmptyFullSync governs the empty-snapshot guardrail (§1.8, must-fix), mirroring
	// the mesh collector: because every Observe is a full sync that drives the host's
	// tombstone-absent sweep, an EMPTY Pod list — far more often a transient/RBAC-
	// degraded/mis-scoped List than a genuinely empty cluster — would otherwise retract
	// software.container from EVERY node and the advisory check would go silent on real
	// vulnerable images (failure presented as "no findings"). By default (false) an empty
	// snapshot is treated as suspect: the cycle holds steady (no full-sync boundary, so no
	// tombstone) and logs loudly, self-healing on the next non-empty read.
	AllowEmptyFullSync bool
}

// Server implements the sovereign plugin port for the kubecontainers Syncer (ADR-0080):
// it OBSERVEs running Pods and projects the container-image inventory of the software
// dimension onto the nodes that run them. The plugin holds no graph write path (§1.2) —
// it proposes typed ObservedEntity values; the core-side host governs writes (ownership
// of the software.container namespace, identity gating, Run provenance) and the per-
// source full-sync tombstone of nodes it no longer reports.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg    ServerConfig
	client kubernetes.Interface
	log    *slog.Logger
}

func NewServer(cfg ServerConfig, client kubernetes.Interface, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "kubecontainers"
	}
	return &Server{cfg: cfg, client: client, log: log.With("plugin", "kubecontainers")}
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: "v1",
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:           []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE},
		// Claims the software.container Facet namespace as its single §2.1 write-owner
		// (the container form; salt owns software.package, a Helm reader would own
		// software.chart — one namespace, one owner each, one form-agnostic check).
		Contracts: []*pluginv1.ContractDecl{
			{SchemaId: "software.container"},
		},
		// Tombstone on the node scheme: a node the K8s API no longer reports (drained,
		// removed) has its container inventory collected via the host's per-source
		// full-sync tombstone. Union liveness (ADR-0042) keeps the host alive if another
		// Source still observes it by a shared scheme.
		TombstoneSchemes: []string{SchemeNode},
	}}, nil
}

// Observe performs a full sync: list every Pod in scope, aggregate container images per
// node, and emit one `host` Entity per node with the full_sync_complete boundary so the
// host tombstones absent nodes. K8s has no built-in change feed here, so every cycle is
// a full enumeration. An empty snapshot is guarded (see ServerConfig.AllowEmptyFullSync)
// so a transient/degraded List cannot masquerade as an empty cluster and mass-tombstone.
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	entities, err := s.enumerate(stream.Context())
	if err != nil {
		return err
	}

	// Empty-snapshot guardrail (§1.8): decline to assert a full sync over zero nodes
	// unless explicitly permitted — emitting FullSyncComplete here would tombstone every
	// node's container inventory on what is most likely a degraded read.
	if len(entities) == 0 && !s.cfg.AllowEmptyFullSync {
		s.log.Warn("empty pod snapshot — declining to assert a full sync (likely a transient/RBAC/scope issue); " +
			"holding existing inventory, will reconcile on the next non-empty read. Set AllowEmptyFullSync for a cluster expected to be empty.")
		return stream.Send(&pluginv1.ObserveResponse{FullSyncComplete: false})
	}

	s.log.Info("full sync", "nodes", len(entities))
	return stream.Send(&pluginv1.ObserveResponse{Entities: entities, FullSyncComplete: true})
}

// enumerate resolves the cluster qualifier, lists Pods in scope, and normalizes them.
// Pure transport + content-expertise; no graph writes.
func (s *Server) enumerate(ctx context.Context) ([]*pluginv1.ObservedEntity, error) {
	clusterID, err := s.clusterID(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.client.CoreV1().Pods(s.cfg.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	pods := make([]Pod, 0, len(list.Items))
	for i := range list.Items {
		pods = append(pods, fromCoreV1(&list.Items[i]))
	}
	cfg := s.cfg.Config
	cfg.ClusterID = clusterID
	return Normalize(pods, cfg), nil
}

// clusterID resolves the globally-unique cluster qualifier for node identity (§1.2,
// must-fix): an operator-set id when configured, else the authoritative kube-system
// namespace UID (stable for the cluster's lifetime). It NEVER falls back to an empty
// qualifier — an ambiguous node identity that could silently merge two clusters' nodes
// is a projection error, so a resolution failure fails the sync loudly (§1.8) rather
// than emit unqualified identities.
func (s *Server) clusterID(ctx context.Context) (string, error) {
	if s.cfg.ClusterID != "" {
		return s.cfg.ClusterID, nil
	}
	ns, err := s.client.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("kubecontainers: resolve cluster id from kube-system UID (set ClusterID to override): %w", err)
	}
	uid := string(ns.UID)
	if uid == "" {
		return "", fmt.Errorf("kubecontainers: kube-system namespace has no UID; set ClusterID explicitly")
	}
	return uid, nil
}

// fromCoreV1 maps a Kubernetes Pod onto the client-go-free projection shape the
// normalizer consumes (the module-isolation seam: only this file touches client-go).
// The spec image gives the reference; the matching status ContainerStatus gives the
// resolved imageID (the digest). Both init and regular containers are inventoried.
func fromCoreV1(pod *corev1.Pod) Pod {
	imageID := map[string]string{}
	for _, cs := range pod.Status.ContainerStatuses {
		imageID[cs.Name] = cs.ImageID
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		imageID[cs.Name] = cs.ImageID
	}

	containers := make([]Container, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	for _, c := range pod.Spec.InitContainers {
		containers = append(containers, Container{Image: c.Image, ImageID: imageID[c.Name]})
	}
	for _, c := range pod.Spec.Containers {
		containers = append(containers, Container{Image: c.Image, ImageID: imageID[c.Name]})
	}
	return Pod{
		Namespace:  pod.Namespace,
		Name:       pod.Name,
		NodeName:   pod.Spec.NodeName,
		Containers: containers,
	}
}
