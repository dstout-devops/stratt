package provision

import (
	"encoding/json"
	"reflect"
	"testing"
)

// BuildLaunchParams is the fix for a live defect (ADR-0120 D2): `count: 2` expands to web-01 and
// web-02 and raises two Findings, but the build Workflow had no typed channel to receive WHICH
// instance, so it hardcoded web-01 and the gated path could not build the second at all.
//
// These are pure, so they run in `task ci` — the reconcile around them needs a substrate, and a
// Postgres-gated test of the one function that decides per-instance identity would be skipped
// exactly where it matters.

func fleet() Intent {
	return Intent{Name: "web-fleet", Spec: ComputeSpec{
		Count: 2, NamePrefix: "web", ProjectKind: "host",
		Labels: map[string]string{"fleet": "web"},
		Params: map[string]any{"region": "us-east-1", "ami": "ami-0"},
	}}
}

// The whole point: each instance gets ITS OWN name, and the correlation label matches it. If these
// two ever disagree the build produces a host nobody asked for and a Finding that never resolves,
// because the reconcile matches on the label.
func TestEachInstanceGetsItsOwnIdentityAndMatchingCorrelationLabel(t *testing.T) {
	in := fleet()
	for _, d := range desired(in) {
		p := BuildLaunchParams(in, d)
		if p["instance"] != d.Name {
			t.Fatalf("instance: got %v, want %s", p["instance"], d.Name)
		}
		labels, ok := p["labels"].(map[string]any)
		if !ok {
			t.Fatalf("labels must be a JSON-canonical object, got %T", p["labels"])
		}
		if labels[InstanceLabel] != d.Name {
			t.Fatalf("the correlation label must match the instance (%s), got %v", d.Name, labels[InstanceLabel])
		}
		if labels["fleet"] != "web" {
			t.Fatalf("the Intent's own labels must survive: %v", labels)
		}
	}
	// And they are actually different, which is the defect: web-01 and web-02, not web-01 twice.
	a := BuildLaunchParams(in, desired(in)[0])["instance"]
	b := BuildLaunchParams(in, desired(in)[1])["instance"]
	if a == b {
		t.Fatalf("two instances resolved to the same identity (%v) — the count>1 defect", a)
	}
}

// Deriving the label must not MUTATE the Intent's own label map: the spec is shared across every
// instance of the fleet, so writing through it would give instance 2 instance 1's correlation
// label and silently corrupt every subsequent build.
func TestDerivingTheLabelDoesNotMutateTheIntent(t *testing.T) {
	in := fleet()
	_ = BuildLaunchParams(in, Instance{Name: "web-01", Ordinal: 1})
	if _, leaked := in.Spec.Labels[InstanceLabel]; leaked {
		t.Fatal("BuildLaunchParams wrote the correlation label back into the shared Intent spec")
	}
	if len(in.Spec.Labels) != 1 {
		t.Fatalf("the Intent's labels were modified: %v", in.Spec.Labels)
	}
}

// `params` passes through WHOLE, never flattened into siblings. Flattening would force every build
// Workflow's CLOSED inputs schema to enumerate provider-shaped keys (region/ami/instanceType), so
// adding one Intent param would break the launch until an estate Workflow was edited — the provider
// coupling ADR-0110 removed, reintroduced one layer down (§1.1/§1.5).
func TestProviderParamsPassThroughOpaque(t *testing.T) {
	in := fleet()
	p := BuildLaunchParams(in, Instance{Name: "web-01", Ordinal: 1})
	got, ok := p["params"].(map[string]any)
	if !ok {
		t.Fatalf("params must stay one opaque object, got %T", p["params"])
	}
	if !reflect.DeepEqual(got, in.Spec.Params) {
		t.Fatalf("params must pass through unchanged: %v", got)
	}
	// The provider's own keys must NOT appear at the top level — that is what flattening would do.
	for _, k := range []string{"region", "ami", "instanceType"} {
		if _, leaked := p[k]; leaked {
			t.Errorf("provider key %q leaked to the top level; params must stay namespaced", k)
		}
	}
}

// Placement rides through with its fields DISTINCT per topology kind (ADR-0059 D3 refused a generic
// `zone` string so a build never disambiguates the edge type by resolving its target's kind), and
// only declared fields are emitted so a Workflow can tell "not declared" from "declared empty".
func TestPlacementRidesThroughWithDistinctFields(t *testing.T) {
	in := fleet()
	in.Spec.Placement = &Placement{Subnet: "app-subnet", AvailabilityZone: "us-east-1a"}
	p := BuildLaunchParams(in, Instance{Name: "web-01", Ordinal: 1})
	place, ok := p["placement"].(map[string]any)
	if !ok {
		t.Fatalf("placement must be a JSON-canonical object, got %T", p["placement"])
	}
	if place["subnet"] != "app-subnet" || place["availabilityZone"] != "us-east-1a" {
		t.Fatalf("placement: %v", place)
	}
	// ADR-0123 D2 WITHDREW the omit-when-undeclared half of ADR-0059 D3: an undeclared field is
	// present-and-empty, because omitting it made {{.launch.placement.*}} unsafe in a shared builder
	// (the key vanishes, the substituter fails closed) — which is why placement was declared by
	// seven build Workflows and bound by none. The distinction it protected was unusable anyway:
	// there are no conditionals in the template language, so no builder could branch on presence.
	if v, present := place["dmz"]; !present || v != "" {
		t.Errorf("an undeclared placement field must be present and EMPTY: %v", place)
	}
	if _, generic := place["zone"]; generic {
		t.Error("there is no generic `zone` — ADR-0059 D3 refused it, and ADR-0123 D2 keeps that half")
	}
	// No placement declared ⇒ the key is still PRESENT, with every field empty. See above.
	in.Spec.Placement = nil
	bare, ok := BuildLaunchParams(in, Instance{Name: "web-01"})["placement"].(map[string]any)
	if !ok {
		t.Fatal("placement must always be sent, so a shared builder can bind it unconditionally")
	}
	if bare["subnet"] != "" || bare["dmz"] != "" || bare["availabilityZone"] != "" {
		t.Errorf("an unplaced Intent's placement must be all-empty: %v", bare)
	}
}

// The SAME rule, on the sibling key that was left behind: `params` is present-and-empty too.
//
// It was emitted only `if len(Spec.Params) > 0`, which is the exact shape ADR-0123 D2 withdrew one
// field over — and for the exact reason, since the substituter fails closed on a vanished key and
// the template language has no conditionals to guard it with. It survived only because every
// provisioning Intent in the estate happened to declare params. `web-fleet` on the kubernetes
// substrate legitimately declares none (kubecompute reads none of them and says so), and with the
// key gone the ESTATE was refused for a builder's honest input declaration.
//
// Both shapes are checked here, fleet and singleton, because they are two functions with one rule.
func TestParamsAreAlwaysSentEvenWhenTheIntentDeclaresNone(t *testing.T) {
	in := fleet()
	in.Spec.Params = nil
	p, ok := BuildLaunchParams(in, Instance{Name: "web-01", Ordinal: 1})["params"].(map[string]any)
	if !ok {
		t.Fatal("params must always be sent, so a shared builder can bind {{.launch.params}} unconditionally")
	}
	if len(p) != 0 {
		t.Errorf("an Intent declaring no params must send an EMPTY object, not invented content: %v", p)
	}
	si := SingletonIntent{Name: "app-subnet", Kind: "Intent/Subnet", Spec: SingletonSpec{ProjectKind: "subnet"}}
	sp, ok := SingletonLaunchParams(si, Instance{Name: "Intent/Subnet/app-subnet"})["params"].(map[string]any)
	if !ok || len(sp) != 0 {
		t.Errorf("the singleton path must follow the same rule: %v", sp)
	}
}

// The params are stored as jsonb and read back as map[string]any, so the in-memory shape must
// survive a JSON round-trip unchanged. A map[string]string or a struct pointer would resolve as a
// whole value and then fail the moment a template addressed a field inside it — far from the cause.
func TestParamsAreJSONCanonical(t *testing.T) {
	in := fleet()
	in.Spec.Placement = &Placement{AvailabilityZone: "us-east-1a"}
	before := BuildLaunchParams(in, Instance{Name: "web-02", Ordinal: 2})

	raw, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatal(err)
	}
	// ordinal is the one value JSON changes (int → float64); everything else must be identical,
	// which is what makes the stored and in-memory params interchangeable at launch.
	before["ordinal"] = float64(before["ordinal"].(int))
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("params changed shape across a JSON round-trip:\n before: %#v\n after:  %#v", before, after)
	}
}
