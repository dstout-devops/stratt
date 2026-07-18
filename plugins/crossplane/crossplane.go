// Package crossplane is the Crossplane build Actuator (ADR-0059): Crossplane is a
// Kubernetes control plane that provisions infrastructure from declarative Claims.
// This plugin is the `builder:` an Intent/Subnet (or any network Intent) names — it
// applies a Crossplane Claim, waits for it to become Ready, and projects the
// provisioned resource back as an Entity over the sovereign plugin port (ADR-0046).
// The control plane governs what it may write; the plugin holds no graph path.
//
// It is landscape-agnostic BY the Claim: the Intent's opaque `params` carry the
// Claim's GVR + spec (§1.5, never typed per-provider), so the same Actuator provisions
// a subnet on AWS, GCP, or vSphere depending only on which Composition the cluster has.
package crossplane

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

const protocolVersion = "v1"

// Config locates the Kubernetes cluster whose Crossplane provisions the estate.
type Config struct {
	PluginID   string
	Kubeconfig string // path; "" ⇒ in-cluster config
}

// Server implements the sovereign plugin port for the Crossplane build Actuator.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg Config
	log *slog.Logger
	// dyn resolves the dynamic client lazily (tests inject a fake).
	dyn func() (dynamic.Interface, error)
}

func NewServer(cfg Config, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, log: log.With("plugin", "crossplane")}
	if s.cfg.PluginID == "" {
		s.cfg.PluginID = "crossplane"
	}
	s.dyn = s.buildClient
	return s
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: protocolVersion,
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_ACTUATOR,
		Verbs:           []pluginv1.Verb{pluginv1.Verb_VERB_APPLY, pluginv1.Verb_VERB_DESTROY},
		Capabilities:    []string{"apply.dry-run"},
	}}, nil
}

func (s *Server) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	return &pluginv1.HealthResponse{Status: pluginv1.HealthResponse_SERVING_UP, ProtocolVersion: protocolVersion}, nil
}

// claimParams is the Actuator's opaque input (validated against ITS Contract, not
// typed per-provider, §1.5): the Crossplane Claim to apply + how the built resource
// projects back into the estate (ADR-0059 §6 overlay).
type claimParams struct {
	Group        string         `json:"group"`     // e.g. "net.example.org"
	Version      string         `json:"version"`   // e.g. "v1alpha1"
	Resource     string         `json:"resource"`  // the plural, e.g. "subnetclaims"
	Kind         string         `json:"kind"`      // e.g. "SubnetClaim"
	Name         string         `json:"name"`      // claim name
	Namespace    string         `json:"namespace"` // "" ⇒ cluster-scoped (an XR, not a Claim)
	Spec         map[string]any `json:"spec"`
	ReadySeconds int            `json:"readySeconds"` // poll budget for the Ready condition (default 120)
	// Estate overlay (ADR-0059 §6): the built resource projects AS this kind with
	// these labels, correlated by identity — Run-provenance, never a reconcile write.
	ProjectKind    string            `json:"projectKind"`    // e.g. "subnet"
	ProjectLabels  map[string]string `json:"projectLabels"`  // e.g. {source: crossplane}
	IdentityScheme string            `json:"identityScheme"` // e.g. "crossplane.claim"
}

// Apply provisions one Crossplane Claim: server-side-apply it, wait for Ready, and
// project the built resource back. It streams progress TaskEvents, then a WriteBack
// with the projected Entity, then the terminal ItemResult (§1.8 descent).
func (s *Server) Apply(req *pluginv1.ApplyRequest, stream grpc.ServerStreamingServer[pluginv1.ApplyResponse]) error {
	ctx := stream.Context()
	var p claimParams
	if err := json.Unmarshal(req.GetDesired().GetBytes(), &p); err != nil {
		return terminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "invalid params: "+err.Error())
	}
	if p.Group == "" || p.Version == "" || p.Resource == "" || p.Kind == "" || p.Name == "" {
		return terminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "group, version, resource, kind, name are required")
	}
	gvr := schema.GroupVersionResource{Group: p.Group, Version: p.Version, Resource: p.Resource}

	dyn, err := s.dyn()
	if err != nil {
		return terminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "kube client: "+err.Error())
	}
	ri := resourceClient(dyn, gvr, p.Namespace)

	_ = stream.Send(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(),
		Message: fmt.Sprintf("applying %s/%s claim %q", p.Group, p.Kind, p.Name),
		Fields:  map[string]string{"resource": p.Resource, "name": p.Name},
	}})

	claim := buildClaim(p)
	if req.GetDryRun() {
		data, _ := json.Marshal(claim.Object)
		if _, err := ri.Patch(ctx, p.Name, types.ApplyPatchType, data, metav1.PatchOptions{
			FieldManager: "stratt-crossplane", DryRun: []string{metav1.DryRunAll},
		}); err != nil {
			return terminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "dry-run apply: "+err.Error())
		}
		return terminal(stream, true, pluginv1.ItemResult_STATUS_CHANGED, "dry-run: claim would apply")
	}

	data, _ := json.Marshal(claim.Object)
	if _, err := ri.Patch(ctx, p.Name, types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: "stratt-crossplane", Force: boolPtr(true),
	}); err != nil {
		return terminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "apply: "+err.Error())
	}

	ready, got, err := waitReady(ctx, ri, p.Name, budget(p.ReadySeconds))
	if err != nil {
		return terminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "await ready: "+err.Error())
	}
	if !ready {
		return terminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "claim did not become Ready within the budget")
	}

	ent := project(p, got)
	s.log.Info("crossplane claim ready", "name", p.Name, "projectKind", ent.GetKind())
	_ = stream.Send(&pluginv1.ApplyResponse{
		Event:     &pluginv1.TaskEvent{Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(), Message: "provisioned", Fields: map[string]string{"kind": "writeback"}},
		WriteBack: []*pluginv1.ObservedEntity{ent},
	})
	return terminal(stream, true, pluginv1.ItemResult_STATUS_CHANGED, "claim Ready: "+p.Name)
}

// buildClaim renders the unstructured Crossplane Claim from params (pure).
func buildClaim(p claimParams) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": p.Group + "/" + p.Version,
		"kind":       p.Kind,
		"metadata":   map[string]any{"name": p.Name},
		"spec":       p.Spec,
	}
	if p.Namespace != "" {
		obj["metadata"].(map[string]any)["namespace"] = p.Namespace
	}
	return &unstructured.Unstructured{Object: obj}
}

// isReady reports whether a Crossplane resource's status carries a Ready=True
// condition (the Crossplane readiness convention). Pure.
func isReady(u *unstructured.Unstructured) bool {
	conds, found, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cm["type"] == "Ready" && cm["status"] == "True" {
			return true
		}
	}
	return false
}

// project maps the applied/ready Claim to an ObservedEntity carrying the estate
// overlay (ADR-0059 §6): kind + labels + the correlation identity. Pure.
func project(p claimParams, got *unstructured.Unstructured) *pluginv1.ObservedEntity {
	kind := p.ProjectKind
	if kind == "" {
		kind = "subnet"
	}
	scheme := p.IdentityScheme
	if scheme == "" {
		scheme = "crossplane.claim"
	}
	id := p.Name
	if p.Namespace != "" {
		id = p.Namespace + "/" + p.Name
	}
	labels := map[string]string{}
	for k, v := range p.ProjectLabels {
		labels[k] = v
	}
	// Crossplane KNOWS the subnet it just built — it projects the net.subnet Facet
	// too (the as-built CIDR + the Claim it came from). A full-featured plugin
	// projects everything its system reports; the fact that NetBox ALSO knows about
	// subnets is not a reason to strip this — it is resolved by multi-source Facet
	// ownership (ADR-0060), never by crippling the builder.
	facet := map[string]any{"claim": p.Kind, "name": p.Name}
	// Prefer the AS-BUILT cidr Crossplane reports in status; fall back to the
	// requested spec. (A full-featured builder reports what it actually made.)
	if cidr, found, _ := unstructured.NestedString(got.Object, "status", "cidr"); found && cidr != "" {
		facet["cidr"] = cidr
	} else if cidr, ok := p.Spec["cidr"].(string); ok && cidr != "" {
		facet["cidr"] = cidr
	}
	raw, _ := json.Marshal(facet)
	return &pluginv1.ObservedEntity{
		Kind:         kind,
		IdentityKeys: map[string]string{scheme: id},
		Labels:       labels,
		Facets:       map[string][]byte{"net.subnet": raw},
	}
}

func waitReady(ctx context.Context, ri dynamic.ResourceInterface, name string, budget time.Duration) (bool, *unstructured.Unstructured, error) {
	deadline := time.Now().Add(budget)
	for {
		got, err := ri.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil, err
		}
		if isReady(got) {
			return true, got, nil
		}
		if time.Now().After(deadline) {
			return false, got, nil
		}
		select {
		case <-ctx.Done():
			return false, got, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func resourceClient(dyn dynamic.Interface, gvr schema.GroupVersionResource, ns string) dynamic.ResourceInterface {
	if ns == "" {
		return dyn.Resource(gvr)
	}
	return dyn.Resource(gvr).Namespace(ns)
}

func terminal(stream grpc.ServerStreamingServer[pluginv1.ApplyResponse], ok bool, status pluginv1.ItemResult_Status, msg string) error {
	return stream.Send(&pluginv1.ApplyResponse{
		Event:  &pluginv1.TaskEvent{Terminal: true, Ok: ok, At: timestamppb.Now(), Message: msg, Fields: map[string]string{"kind": "finished"}},
		Result: &pluginv1.ItemResult{ItemKey: "", Status: status},
	})
}

func budget(sec int) time.Duration {
	if sec <= 0 {
		sec = 120
	}
	return time.Duration(sec) * time.Second
}

func boolPtr(b bool) *bool { return &b }

// buildClient resolves the dynamic client from in-cluster config (default) or the
// configured kubeconfig path — the cluster whose Crossplane provisions the estate.
func (s *Server) buildClient() (dynamic.Interface, error) {
	var cfg *rest.Config
	var err error
	if s.cfg.Kubeconfig != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", s.cfg.Kubeconfig)
	} else {
		cfg, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("crossplane: kube config: %w", err)
	}
	return dynamic.NewForConfig(cfg)
}
