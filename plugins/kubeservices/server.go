package kubeservices

import (
	"context"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Config locates the collection scope.
type Config struct {
	PluginID      string // the authenticated channel identity the operator grant is keyed on
	Namespace     string // "" ⇒ all namespaces
	ClusterDomain string // service DNS suffix (default "cluster.local"); forms each service's dns.fqdn identity
}

// Server implements the sovereign plugin port for the kubeservices Syncer (ADR-0081):
// it OBSERVEs Kubernetes Services and projects the service/capability dimension. The
// plugin holds no graph write path (§1.2) — it proposes typed ObservedEntity/
// ObservedRelation values; the core-side host governs writes (ownership, identity
// gating, Run provenance) and does the per-source full-sync `provides`-edge replace.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg    Config
	client kubernetes.Interface
	log    *slog.Logger
}

func NewServer(cfg Config, client kubernetes.Interface, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "kubeservices"
	}
	return &Server{cfg: cfg, client: client, log: log.With("plugin", "kubeservices")}
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: "v1",
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:           []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE},
		Contracts: []*pluginv1.ContractDecl{
			{SchemaId: "service.endpoint"},
			{SchemaId: "software.chart"},
		},
		// Tombstone on the plugin's OWN identity schemes: a Service/release the K8s
		// API no longer reports is a kubeservices-scoped removal (its `provides` edges
		// are collected by the host's per-source full-sync replace + the endpoint-
		// tombstone cascade).
		TombstoneSchemes: []string{SchemeService, SchemeRelease},
	}}, nil
}

// Observe performs a full sync: list every Service in scope, map it onto the
// projection shape, and emit service/application Entities + the `provides` edge with
// the full_sync_complete boundary so the host tombstones absent Services/releases
// and replaces this Source's `provides` edges (ADR-0081). K8s has no built-in Service
// change feed here, so every cycle is a full enumeration.
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	entities, err := s.enumerate(stream.Context())
	if err != nil {
		return err
	}
	s.log.Info("full sync", "entities", len(entities))
	return stream.Send(&pluginv1.ObserveResponse{Entities: entities, FullSyncComplete: true})
}

// enumerate lists Services in scope and normalizes them. Pure transport + content-
// expertise; no graph writes.
func (s *Server) enumerate(ctx context.Context) ([]*pluginv1.ObservedEntity, error) {
	list, err := s.client.CoreV1().Services(s.cfg.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	services := make([]K8sService, 0, len(list.Items))
	for i := range list.Items {
		services = append(services, fromCoreV1(&list.Items[i]))
	}
	return Normalize(services, s.cfg.ClusterDomain), nil
}

// fromCoreV1 maps a Kubernetes Service onto the client-go-free projection shape the
// normalizer consumes (the module-isolation seam: only this file touches client-go).
func fromCoreV1(svc *corev1.Service) K8sService {
	ports := make([]ServicePort, 0, len(svc.Spec.Ports))
	for _, p := range svc.Spec.Ports {
		ports = append(ports, ServicePort{
			Name:       p.Name,
			Port:       int(p.Port),
			TargetPort: p.TargetPort.String(),
			Protocol:   string(p.Protocol), // K8s reports L4 (TCP/UDP/SCTP); L7 refined by annotations later
		})
	}
	return K8sService{
		Namespace: svc.Namespace,
		Name:      svc.Name,
		Type:      string(svc.Spec.Type),
		ClusterIP: svc.Spec.ClusterIP,
		Ports:     ports,
		Selector:  svc.Spec.Selector,
		Labels:    svc.Labels,
	}
}
