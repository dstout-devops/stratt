package crossplane

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func params() claimParams {
	return claimParams{
		Group: "net.example.org", Version: "v1alpha1", Resource: "subnetclaims",
		Kind: "SubnetClaim", Name: "web-dmz", Namespace: "stratt",
		Spec:           map[string]any{"cidr": "10.0.1.0/24"},
		ProjectKind:    "subnet",
		ProjectLabels:  map[string]string{"source": "crossplane", "fleet": "web"},
		IdentityScheme: "crossplane.claim",
	}
}

func TestBuildClaim(t *testing.T) {
	u := buildClaim(params())
	if u.GetAPIVersion() != "net.example.org/v1alpha1" || u.GetKind() != "SubnetClaim" {
		t.Errorf("apiVersion/kind = %s/%s", u.GetAPIVersion(), u.GetKind())
	}
	if u.GetName() != "web-dmz" || u.GetNamespace() != "stratt" {
		t.Errorf("name/ns = %s/%s", u.GetName(), u.GetNamespace())
	}
	spec, _, _ := unstructured.NestedMap(u.Object, "spec")
	if spec["cidr"] != "10.0.1.0/24" {
		t.Errorf("spec.cidr = %v", spec["cidr"])
	}
}

func TestIsReady(t *testing.T) {
	notReady := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{
		"conditions": []any{map[string]any{"type": "Synced", "status": "True"}}}}}
	ready := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{
		"conditions": []any{
			map[string]any{"type": "Synced", "status": "True"},
			map[string]any{"type": "Ready", "status": "True"},
		}}}}
	if isReady(notReady) {
		t.Error("no Ready=True condition → not ready")
	}
	if !isReady(ready) {
		t.Error("Ready=True condition → ready")
	}
}

// project carries the ADR-0059 §6 overlay: kind=subnet, the correlation identity,
// and the operator labels — the built resource joins the estate.
func TestProject(t *testing.T) {
	ent := project(params(), &unstructured.Unstructured{})
	if ent.GetKind() != "subnet" {
		t.Errorf("kind = %q, want subnet", ent.GetKind())
	}
	if ent.GetIdentityKeys()["crossplane.claim"] != "stratt/web-dmz" {
		t.Errorf("identity = %v", ent.GetIdentityKeys())
	}
	if ent.GetLabels()["fleet"] != "web" || ent.GetLabels()["source"] != "crossplane" {
		t.Errorf("labels = %v", ent.GetLabels())
	}
}

// waitReady polls the live resource until the Ready condition flips — proven with a
// fake dynamic client seeded with a Ready claim (Crossplane's controllers stand in).
func TestWaitReadyAgainstFake(t *testing.T) {
	p := params()
	gvr := schema.GroupVersionResource{Group: p.Group, Version: p.Version, Resource: p.Resource}
	readyObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": p.Group + "/" + p.Version, "kind": p.Kind,
		"metadata": map[string]any{"name": p.Name, "namespace": p.Namespace},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "SubnetClaimList"}, readyObj)

	ok, got, err := waitReady(context.Background(), resourceClient(dyn, gvr, p.Namespace), p.Name, 3*time.Second)
	if err != nil {
		t.Fatalf("waitReady: %v", err)
	}
	if !ok || got == nil {
		t.Fatalf("expected the seeded claim to be Ready, got ok=%v", ok)
	}
}

// observeStreamMock captures the ObserveResponse stream for a unit test.
type observeStreamMock struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*pluginv1.ObserveResponse
}

func (m *observeStreamMock) Context() context.Context { return m.ctx }
func (m *observeStreamMock) Send(r *pluginv1.ObserveResponse) error {
	m.sent = append(m.sent, r)
	return nil
}

// TestObserveProjectsClaims proves the SYNCER half: Crossplane enumerates its live
// Claims and projects each as a subnet Entity carrying the AS-BUILT cidr (status wins
// over the requested spec) — the resync-able Source the guardian pointed to, distinct
// from the Apply write-back (this row is Syncer-provenance, authority-resolvable).
func TestObserveProjectsClaims(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "net.example.org", Version: "v1alpha1", Resource: "subnetclaims"}
	claim := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "net.example.org/v1alpha1", "kind": "SubnetClaim",
		"metadata": map[string]any{"name": "web-dmz", "namespace": "stratt"},
		"spec":     map[string]any{"cidr": "10.0.1.0/24"},
		"status":   map[string]any{"cidr": "10.0.1.0/25"}, // as-built differs from requested
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "SubnetClaimList"}, claim)

	s := NewServer(Config{ObserveClaims: []ObserveClaim{{
		Group: "net.example.org", Version: "v1alpha1", Resource: "subnetclaims",
		Namespace: "stratt", ProjectKind: "subnet", IdentityScheme: "crossplane.claim",
	}}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.dyn = func() (dynamic.Interface, error) { return dyn, nil }

	stream := &observeStreamMock{ctx: context.Background()}
	if err := s.Observe(&pluginv1.ObserveRequest{}, stream); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(stream.sent) != 1 || !stream.sent[0].GetFullSyncComplete() {
		t.Fatalf("expected one full-sync response, got %d", len(stream.sent))
	}
	ents := stream.sent[0].GetEntities()
	if len(ents) != 1 {
		t.Fatalf("expected 1 observed entity, got %d", len(ents))
	}
	e := ents[0]
	if e.GetKind() != "subnet" || e.GetIdentityKeys()["crossplane.claim"] != "stratt/web-dmz" {
		t.Errorf("kind/identity = %s / %v", e.GetKind(), e.GetIdentityKeys())
	}
	if e.GetLabels()["source"] != "crossplane" {
		t.Errorf("source label = %v", e.GetLabels())
	}
	var facet map[string]any
	if err := json.Unmarshal(e.GetFacets()["net.subnet"], &facet); err != nil {
		t.Fatalf("facet unmarshal: %v", err)
	}
	if facet["cidr"] != "10.0.1.0/25" { // as-built (status) wins over spec
		t.Errorf("net.subnet cidr = %v, want as-built 10.0.1.0/25", facet["cidr"])
	}
}

// TestManifestDualVerb proves the plugin advertises OBSERVE alongside APPLY/DESTROY —
// the full-featured dual-verb surface the host gates each grant against.
func TestManifestDualVerb(t *testing.T) {
	s := NewServer(Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m, err := s.GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	verbs := map[pluginv1.Verb]bool{}
	for _, v := range m.GetManifest().GetVerbs() {
		verbs[v] = true
	}
	for _, want := range []pluginv1.Verb{pluginv1.Verb_VERB_APPLY, pluginv1.Verb_VERB_DESTROY, pluginv1.Verb_VERB_OBSERVE} {
		if !verbs[want] {
			t.Errorf("manifest missing verb %v", want)
		}
	}
}

// invokeStreamMock captures the InvokeResponse stream.
type invokeStreamMock struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*pluginv1.InvokeResponse
}

func (m *invokeStreamMock) Context() context.Context { return m.ctx }
func (m *invokeStreamMock) Send(r *pluginv1.InvokeResponse) error {
	m.sent = append(m.sent, r)
	return nil
}

// TestProjectEntity proves the create-subnet Action projects entity-only (ADR-0059):
// kind + the crossplane.claim identity + the stratt.intent/singleton correlation label,
// and NO net.subnet facet (the Syncer supplies that).
func TestProjectEntity(t *testing.T) {
	ent := projectEntity(claimParams{
		Name: "app-subnet", ProjectKind: "subnet", IdentityScheme: "crossplane.claim",
		ProjectLabels: map[string]string{"stratt.intent/singleton": "Intent/Subnet/app-subnet"},
	})
	if ent.GetKind() != "subnet" {
		t.Errorf("kind = %q", ent.GetKind())
	}
	if ent.GetIdentityKeys()["crossplane.claim"] != "app-subnet" {
		t.Errorf("identity = %v", ent.GetIdentityKeys())
	}
	if ent.GetLabels()["stratt.intent/singleton"] != "Intent/Subnet/app-subnet" {
		t.Errorf("correlation label missing: %v", ent.GetLabels())
	}
	if len(ent.GetFacets()) != 0 {
		t.Errorf("Action projection must be entity-only (no facet), got %v", ent.GetFacets())
	}
}

// (The full Invoke provisioning path uses server-side-apply, which the fake dynamic
// client doesn't support — it is verified live end-to-end against real Crossplane in the
// dev cluster. The pure projection + action dispatch are unit-tested here.)

// TestInvokeUnknownAction proves an unknown action is rejected.
func TestInvokeUnknownAction(t *testing.T) {
	s := NewServer(Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	err := s.Invoke(&pluginv1.InvokeRequest{Action: "crossplane/delete-everything"}, &invokeStreamMock{ctx: context.Background()})
	if err == nil {
		t.Fatal("unknown action must be rejected")
	}
}

// TestProjectEntityRelations proves the build projects its topology edges (ADR-0059):
// a host placed-in a subnet, targeted by identity.
func TestProjectEntityRelations(t *testing.T) {
	ent := projectEntity(claimParams{
		Name: "app-01", ProjectKind: "host", IdentityScheme: "crossplane.claim",
		Relations: []relationParam{{Type: "placed-in", ToScheme: "crossplane.claim", ToValue: "app-subnet"}},
	})
	if len(ent.GetRelations()) != 1 {
		t.Fatalf("expected 1 relation, got %d", len(ent.GetRelations()))
	}
	r := ent.GetRelations()[0]
	if r.GetType() != "placed-in" || r.GetToScheme() != "crossplane.claim" || r.GetToValue() != "app-subnet" {
		t.Errorf("relation = %s %s=%s", r.GetType(), r.GetToScheme(), r.GetToValue())
	}
}
