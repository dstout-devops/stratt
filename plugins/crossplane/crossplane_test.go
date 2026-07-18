package crossplane

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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
