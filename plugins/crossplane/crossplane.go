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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// actionCreateSubnet is the targetless builder verb an Intent/Subnet launches (ADR-0059).
const actionCreateSubnet = "crossplane/create-subnet"

// Config locates the Kubernetes cluster whose Crossplane provisions the estate.
type Config struct {
	PluginID   string
	Kubeconfig string // path; "" ⇒ in-cluster config
	// ObserveClaims are the Crossplane Claim/XR kinds this plugin enumerates as a
	// SYNCER (ADR-0060): Crossplane KNOWS the resources it built, so it observes
	// their as-built state back as a registered Source — the full-featured dual-verb
	// plugin (it BUILDS via Apply and OBSERVES via Observe). Empty ⇒ observe nothing
	// (a build-only deployment); Observe then streams an empty full-sync.
	ObserveClaims []ObserveClaim
}

// ObserveClaim declares one Claim/XR kind to enumerate and how each instance
// projects into the estate — the read-side twin of claimParams' project overlay.
type ObserveClaim struct {
	Group          string `json:"group"`
	Version        string `json:"version"`
	Resource       string `json:"resource"`       // the plural, e.g. "subnetclaims"
	Namespace      string `json:"namespace"`      // "" ⇒ cluster-scoped (an XR)
	ProjectKind    string `json:"projectKind"`    // e.g. "subnet"
	IdentityScheme string `json:"identityScheme"` // e.g. "crossplane.claim"
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
		// Class is the PRIMARY declared kind (the builder); Verbs is the authoritative
		// capability surface. This is a FULL-FEATURED dual-verb plugin — it BUILDS
		// Claims (Apply/Destroy) AND OBSERVES their as-built state (Observe), so its
		// real CIDRs are resync-able + authority-declarable (ADR-0060), never a
		// synthesized Actuator write-back source. The host gates each grant on the verb.
		Class: pluginv1.PluginClass_PLUGIN_CLASS_ACTUATOR,
		// APPLY/DESTROY (actuator, View-targeted reconcile), OBSERVE (syncer), and INVOKE
		// (the create-subnet Action — the targetless builder an Intent/Subnet launches,
		// ADR-0059). One binary, the whole build+observe lifecycle.
		Verbs: []pluginv1.Verb{pluginv1.Verb_VERB_APPLY, pluginv1.Verb_VERB_DESTROY, pluginv1.Verb_VERB_OBSERVE, pluginv1.Verb_VERB_INVOKE},
		// Observe REQUESTS to own net.subnet (owned-but-uncovered, §1.1 — no schema
		// until a Contract consumes it). NetBox is declared authoritative for it
		// (ADR-0060); Crossplane's as-built CIDR is retained, resolvable signal.
		Contracts:        []*pluginv1.ContractDecl{{SchemaId: "net.subnet"}},
		ObserveMode:      pluginv1.Manifest_OBSERVE_MODE_POLL,
		TombstoneSchemes: []string{"crossplane.claim"},
		Actions: []*pluginv1.ActionDecl{{
			Name:        actionCreateSubnet,
			Input:       &pluginv1.ContractRef{SchemaId: "actions/crossplane/create-subnet.input"},
			Output:      &pluginv1.ContractRef{SchemaId: "actions/crossplane/create-subnet.output"},
			Idempotent:  true, // server-side-apply of the named resource is idempotent
			DryRunnable: true,
		}},
		Capabilities: []string{"apply.dry-run"},
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
	// Cidr is the net.subnet value the build projects (the declared as-built cidr) when
	// it is not readable from the provisioned resource's status/spec — the Intent knows
	// the CIDR it asked for. Optional; the Syncer refreshes net.subnet from the resource.
	Cidr string `json:"cidr"`
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
	_ = stream.Send(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(),
		Message: fmt.Sprintf("applying %s/%s claim %q", p.Group, p.Kind, p.Name),
		Fields:  map[string]string{"resource": p.Resource, "name": p.Name},
	}})

	if req.GetDryRun() {
		if _, err := s.provision(ctx, p, true); err != nil {
			return terminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "dry-run apply: "+err.Error())
		}
		return terminal(stream, true, pluginv1.ItemResult_STATUS_CHANGED, "dry-run: claim would apply")
	}

	got, err := s.provision(ctx, p, false)
	if err != nil {
		return terminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, err.Error())
	}

	ent := project(p, got)
	s.log.Info("crossplane claim ready", "name", p.Name, "projectKind", ent.GetKind())
	_ = stream.Send(&pluginv1.ApplyResponse{
		Event:     &pluginv1.TaskEvent{Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(), Message: "provisioned", Fields: map[string]string{"kind": "writeback"}},
		WriteBack: []*pluginv1.ObservedEntity{ent},
	})
	return terminal(stream, true, pluginv1.ItemResult_STATUS_CHANGED, "claim Ready: "+p.Name)
}

// provision applies the Crossplane resource (server-side-apply) and waits for it to
// become Ready — the shared build core of the Apply (actuator, View-targeted) and Invoke
// (create-subnet Action, targetless builder) verbs. dryRun applies with DryRunAll and
// returns (nil, nil) without waiting. Landscape-agnostic: the params' GVR + spec name the
// Crossplane resource (a provider-kubernetes Object in dev, a provider-aws Subnet in
// prod), never typed per-provider (§1.5).
func (s *Server) provision(ctx context.Context, p claimParams, dryRun bool) (*unstructured.Unstructured, error) {
	if p.Group == "" || p.Version == "" || p.Resource == "" || p.Kind == "" || p.Name == "" {
		return nil, fmt.Errorf("group, version, resource, kind, name are required")
	}
	dyn, err := s.dyn()
	if err != nil {
		return nil, fmt.Errorf("kube client: %w", err)
	}
	gvr := schema.GroupVersionResource{Group: p.Group, Version: p.Version, Resource: p.Resource}
	ri := resourceClient(dyn, gvr, p.Namespace)
	data, _ := json.Marshal(buildClaim(p).Object)
	opts := metav1.PatchOptions{FieldManager: "stratt-crossplane", Force: boolPtr(true)}
	if dryRun {
		opts = metav1.PatchOptions{FieldManager: "stratt-crossplane", DryRun: []string{metav1.DryRunAll}}
	}
	if _, err := ri.Patch(ctx, p.Name, types.ApplyPatchType, data, opts); err != nil {
		return nil, fmt.Errorf("apply: %w", err)
	}
	if dryRun {
		return nil, nil
	}
	ready, got, err := waitReady(ctx, ri, p.Name, budget(p.ReadySeconds))
	if err != nil {
		return nil, fmt.Errorf("await ready: %w", err)
	}
	if !ready {
		return nil, fmt.Errorf("%s %q did not become Ready within the budget", p.Kind, p.Name)
	}
	return got, nil
}

// Invoke runs the create-subnet Action (ADR-0059): the targetless builder an Intent/Subnet
// launches. It provisions the Crossplane resource (real reconciliation), waits Ready, then
// projects the subnet Entity back with Run provenance — the projectKind + the
// stratt.intent/singleton correlation label ride the projection so the provisioning Finding
// resolves. net.subnet carries the declared cidr; the Syncer refreshes it as-built.
func (s *Server) Invoke(req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	ctx := stream.Context()
	if action := req.GetAction(); action != "" && action != actionCreateSubnet {
		return status.Errorf(codes.InvalidArgument, "crossplane: unknown action %q", action)
	}
	var p claimParams
	if args := req.GetArgs(); args != nil && len(args.GetBytes()) > 0 {
		if err := json.Unmarshal(args.GetBytes(), &p); err != nil {
			return status.Errorf(codes.InvalidArgument, "crossplane/create-subnet: invalid args: %v", err)
		}
	}
	cid := req.GetEnvelope().GetCorrelationId()
	_ = stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(), CorrelationId: cid,
		Message: fmt.Sprintf("provisioning subnet %q via %s/%s", p.Name, p.Group, p.Kind),
		Fields:  map[string]string{"resource": p.Resource, "name": p.Name, "cidr": p.Cidr},
	}})

	got, err := s.provision(ctx, p, req.GetDryRun())
	if err != nil {
		return s.invokeFailed(stream, cid, fmt.Errorf("crossplane/create-subnet: %w", err))
	}
	if req.GetDryRun() {
		return stream.Send(&pluginv1.InvokeResponse{
			Event:  &pluginv1.TaskEvent{Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(), CorrelationId: cid, Terminal: true, Ok: true, Message: "dry-run ok: subnet would provision"},
			Result: &pluginv1.InvokeResult{OutputContract: &pluginv1.ContractRef{SchemaId: "actions/crossplane/create-subnet.output"}},
		})
	}

	_ = got // Ready confirmed; the Syncer's next poll supplies net.subnet (as-built)
	ent := projectEntity(p)
	outputs, _ := json.Marshal(map[string]any{"name": p.Name, "cidr": p.Cidr})
	s.log.Info("crossplane subnet provisioned", "name", p.Name, "projectKind", ent.GetKind())
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(), CorrelationId: cid, Terminal: true, Ok: true, Message: "provisioned " + p.Name, Fields: map[string]string{"name": p.Name, "cidr": p.Cidr}},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: outputs},
			OutputContract: &pluginv1.ContractRef{SchemaId: "actions/crossplane/create-subnet.output"},
			Entities:       []*pluginv1.ObservedEntity{ent},
		},
	})
}

// invokeFailed emits the terminal not-ok TaskEvent (a domain failure rides the typed
// descent channel, §1.8 — not a transport error).
func (s *Server) invokeFailed(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], cid string, cause error) error {
	s.log.Error("create-subnet failed", "error", cause)
	return stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_ERROR, At: timestamppb.Now(), CorrelationId: cid,
		Terminal: true, Ok: false, Message: cause.Error(),
	}})
}

// Observe enumerates every configured Crossplane Claim/XR and streams each as an
// ObservedEntity — the SYNCER half of this full-featured plugin (ADR-0060). Crossplane
// is the registered Source for the resources it built; its as-built CIDR is retained
// alongside NetBox's (the declared authority), never overwriting it. One full-sync window.
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	ctx := stream.Context()
	dyn, err := s.dyn()
	if err != nil {
		return fmt.Errorf("crossplane observe: kube client: %w", err)
	}
	entities := make([]*pluginv1.ObservedEntity, 0)
	for _, oc := range s.cfg.ObserveClaims {
		gvr := schema.GroupVersionResource{Group: oc.Group, Version: oc.Version, Resource: oc.Resource}
		list, err := resourceClient(dyn, gvr, oc.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("crossplane observe %s: %w", oc.Resource, err)
		}
		for i := range list.Items {
			entities = append(entities, projectClaim(oc, &list.Items[i]))
		}
	}
	s.log.Info("crossplane observed", "claim_kinds", len(s.cfg.ObserveClaims), "entities", len(entities))
	return stream.Send(&pluginv1.ObserveResponse{Entities: entities, FullSync: true, FullSyncComplete: true})
}

// projectClaim maps a live Crossplane Claim/XR to an ObservedEntity: the estate kind,
// the crossplane.claim correlation identity (matching the Apply write-back's identity so
// the observed row correlates onto the same Entity), and the net.subnet Facet carrying
// the AS-BUILT cidr. Pure.
func projectClaim(oc ObserveClaim, got *unstructured.Unstructured) *pluginv1.ObservedEntity {
	kind := oc.ProjectKind
	if kind == "" {
		kind = "subnet"
	}
	scheme := oc.IdentityScheme
	if scheme == "" {
		scheme = "crossplane.claim"
	}
	name := got.GetName()
	id := name
	if ns := got.GetNamespace(); ns != "" {
		id = ns + "/" + name
	}
	facet := map[string]any{"claim": got.GetKind(), "name": name}
	if cidr := claimCIDR(got); cidr != "" {
		facet["cidr"] = cidr
	}
	raw, _ := json.Marshal(facet)
	return &pluginv1.ObservedEntity{
		Kind:         kind,
		IdentityKeys: map[string]string{scheme: id},
		Labels:       map[string]string{"source": "crossplane"},
		Facets:       map[string][]byte{"net.subnet": raw},
	}
}

// claimCIDR reads the as-built cidr Crossplane reports (status.cidr) — what it actually
// made — falling back to the requested spec.cidr. Pure.
func claimCIDR(got *unstructured.Unstructured) string {
	// Direct XR/claim: status.cidr (as-built) then spec.cidr (requested).
	if cidr, found, _ := unstructured.NestedString(got.Object, "status", "cidr"); found && cidr != "" {
		return cidr
	}
	// provider-kubernetes Object: the reflected (status.atProvider) then desired
	// (spec.forProvider) wrapped-manifest data.cidr.
	if cidr, found, _ := unstructured.NestedString(got.Object, "status", "atProvider", "manifest", "data", "cidr"); found && cidr != "" {
		return cidr
	}
	if cidr, found, _ := unstructured.NestedString(got.Object, "spec", "forProvider", "manifest", "data", "cidr"); found && cidr != "" {
		return cidr
	}
	if cidr, found, _ := unstructured.NestedString(got.Object, "spec", "cidr"); found && cidr != "" {
		return cidr
	}
	return ""
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

// projectEntity maps the built resource to the ObservedEntity the estate overlay
// declares (ADR-0059 §6): kind + the correlation identity + labels (incl. the
// stratt.intent/singleton correlation key). NO Facet — like awsec2/create-vm, the Action
// projects identity; the Syncer's next poll supplies net.subnet. Pure.
func projectEntity(p claimParams) *pluginv1.ObservedEntity {
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
	return &pluginv1.ObservedEntity{
		Kind:         kind,
		IdentityKeys: map[string]string{scheme: id},
		Labels:       labels,
	}
}

// project is the Apply (actuator) projection: the entity PLUS the net.subnet Facet the
// actuator write-back carries (governed by FacetWriteScope at the host). A full-featured
// plugin projects everything its system reports; net.subnet co-existing with NetBox's is
// resolved by multi-source Facet ownership (ADR-0060), never by crippling the builder. Pure.
func project(p claimParams, got *unstructured.Unstructured) *pluginv1.ObservedEntity {
	ent := projectEntity(p)
	facet := map[string]any{"claim": p.Kind, "name": p.Name}
	// Prefer the AS-BUILT cidr Crossplane reports in status; then the explicit param
	// (the Intent declared it — used when the provisioned resource, e.g. a
	// provider-kubernetes Object, doesn't surface cidr at status.cidr); then the spec.
	if cidr, found, _ := unstructured.NestedString(got.Object, "status", "cidr"); found && cidr != "" {
		facet["cidr"] = cidr
	} else if p.Cidr != "" {
		facet["cidr"] = p.Cidr
	} else if cidr, ok := p.Spec["cidr"].(string); ok && cidr != "" {
		facet["cidr"] = cidr
	}
	raw, _ := json.Marshal(facet)
	ent.Facets = map[string][]byte{"net.subnet": raw}
	return ent
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
