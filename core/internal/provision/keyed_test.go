package provision

import (
	"testing"
)

// keyed builds a zone-spread Intent.
func keyed(zones []string, perZone int) Intent {
	return Intent{Name: "web-fleet", Spec: ComputeSpec{
		NamePrefix: "web", ProjectKind: "host", PerZone: perZone, Zones: zones,
		Labels: map[string]string{"fleet": "web"},
	}}
}

// The property the whole decision exists for (ADR-0123 D1): adding a zone is ADDITIVE. Under
// ADR-0058's positional ordinal, `zones: [a,b]` → `[a,b,c]` renumbers everything after the
// insertion point and the reconcile reads that as destroy-and-recreate — the fleet-wide churn
// ADR-0058 D4 itself flags. This is the test that would fail if identity went back to positional.
func TestAddingAZoneRenumbersNothing(t *testing.T) {
	before := map[string]bool{}
	for _, i := range desired(keyed([]string{"use1a", "use1b"}, 2)) {
		before[i.Name] = true
	}
	if len(before) != 4 {
		t.Fatalf("2 zones x 2 = 4 instances, got %d: %v", len(before), before)
	}

	var added []string
	for _, i := range desired(keyed([]string{"use1a", "use1b", "use1c"}, 2)) {
		if !before[i.Name] {
			added = append(added, i.Name)
		}
		delete(before, i.Name)
	}
	if len(before) != 0 {
		t.Errorf("adding a zone must not change any existing identity; these vanished: %v", before)
	}
	if len(added) != 2 {
		t.Errorf("adding a zone must add exactly perZone instances, got %v", added)
	}
}

// Names are keyed and zero-padded within the zone, so ordering stays lexical per zone and the
// correlation label the next reconcile matches is stable.
func TestKeyedNamesAndZones(t *testing.T) {
	got := desired(keyed([]string{"use1a", "use1b"}, 2))
	want := []string{"web-use1a-01", "web-use1a-02", "web-use1b-01", "web-use1b-02"}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("instance %d = %q, want %q", i, got[i].Name, w)
		}
	}
	// Zone is part of the instance, not something a consumer re-parses out of the name.
	if got[2].Zone != "use1b" || got[2].Ordinal != 1 {
		t.Errorf("the zone and its in-zone ordinal must ride the Instance: %+v", got[2])
	}
}

// DesiredCount is the one place "how big is this fleet" is answered, so the max-delta gate (§4.3)
// and the shortfall arithmetic cannot disagree about it.
func TestDesiredCountCoversBothCardinalities(t *testing.T) {
	if n := keyed([]string{"a", "b", "c"}, 3).Spec.DesiredCount(); n != 9 {
		t.Errorf("keyed: 3 zones x 3 = 9, got %d", n)
	}
	if n := (ComputeSpec{Count: 4}).DesiredCount(); n != 4 {
		t.Errorf("positional: got %d", n)
	}
}

// A keyed instance's zone reaches the provider through placement.availabilityZone (ADR-0123 D2), so
// a zone is declared once — as cardinality — and never restated as placement that could disagree
// with the name (§2.4).
func TestKeyedInstanceCarriesItsZoneAsPlacement(t *testing.T) {
	in := keyed([]string{"use1a", "use1b"}, 1)
	params := BuildLaunchParams(in, desired(in)[1])
	place, ok := params["placement"].(map[string]any)
	if !ok {
		t.Fatalf("placement must always be present: %#v", params["placement"])
	}
	if place["availabilityZone"] != "use1b" {
		t.Errorf("a keyed instance's zone must reach the build: %#v", place)
	}
}

// Placement is emitted COMPLETE even when the Intent declares none (ADR-0123 D2). This is what makes
// {{.launch.placement.subnet}} safe in a SHARED builder: while undeclared fields were omitted, the
// key vanished for an unplaced Intent and the substituter failed closed on it — which is exactly why
// `placement` was declared by seven build Workflows and bound by none of them.
func TestPlacementIsCompleteEvenWhenUndeclared(t *testing.T) {
	in := Intent{Name: "web-fleet", Spec: ComputeSpec{NamePrefix: "web", Count: 1, ProjectKind: "host"}}
	params := BuildLaunchParams(in, desired(in)[0])
	place, ok := params["placement"].(map[string]any)
	if !ok {
		t.Fatalf("an unplaced Intent must still send a placement object, got %#v", params["placement"])
	}
	for _, k := range []string{"subnet", "dmz", "availabilityZone"} {
		v, present := place[k]
		if !present {
			t.Errorf("placement.%s must be present (empty), or a shared builder cannot bind it", k)
		}
		if v != "" {
			t.Errorf("placement.%s must be empty when undeclared, got %#v", k, v)
		}
	}
}

// Excess must recognise a KEYED built name, or a count-down on a zone-spread fleet would see its own
// instances as foreign and tear down nothing (ADR-0114's reach-path runs off this).
func TestExcessRecognisesKeyedNames(t *testing.T) {
	in := keyed([]string{"use1a"}, 1)
	built := map[string]bool{"web-use1a-01": true, "web-use1b-01": true}
	ex := Excess(in, built)
	if len(ex) != 1 || ex[0].Name != "web-use1b-01" {
		t.Fatalf("the instance in a withdrawn zone is the excess, got %+v", ex)
	}
}

// FilterToDeclared is what lets a builder declare only what it consumes (ADR-0123 D3). Before it,
// `additionalProperties: false` plus "the reconcile supplies it" forced a builder to declare every
// param core might send, making an accepted-but-dropped input indistinguishable from a consumed one.
func TestFilterToDeclaredSendsOnlyWhatIsAsked(t *testing.T) {
	full := map[string]any{"instance": "web-01", "ordinal": 1, "placement": map[string]any{}, "params": map[string]any{}}
	got := FilterToDeclared(full, map[string]bool{"instance": true, "params": true})
	if len(got) != 2 || got["instance"] != "web-01" {
		t.Fatalf("only declared keys may be sent: %#v", got)
	}
	// No declared interface ⇒ pass through, and the declaration-time check owns that case: filtering
	// to nothing would silently strip a launch instead of failing in Git review.
	if got := FilterToDeclared(full, nil); len(got) != len(full) {
		t.Errorf("an undeclared interface must pass through for the declaration check to catch: %#v", got)
	}
}
