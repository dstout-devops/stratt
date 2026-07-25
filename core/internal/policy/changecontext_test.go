package policy

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// These cover the change-context admission (ADR-0122), which is a governance seam: what a launcher
// may assert about a change, and what core establishes itself.

// The fail-OPEN direction is the one that had to close. An unknown class means every Control keyed
// on the intended one silently does not fire and the change proceeds. (Break-glass's own typo
// direction happens to be fail-CLOSED — policy.go compares == "emergency", so a misspelled
// emergency leaves the bypassed controls standing. That is why this never bit, and why it was not a
// reason to leave it.)
func TestUnknownChangeClassIsRefused(t *testing.T) {
	err := ValidateChangeContext(map[string]any{types.ChangeContextClassKey: "emergancy"})
	if err == nil {
		t.Fatal("an unknown change class must be refused, not coerced")
	}
	for _, want := range []string{"emergancy", "standard", "emergency"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name the bad value and the valid set; want %q in: %v", want, err)
		}
	}
	for _, cls := range append(types.ChangeClasses, "") {
		if err := ValidateChangeContext(map[string]any{types.ChangeContextClassKey: cls}); err != nil {
			t.Errorf("%q is a valid class (empty = unstated): %v", cls, err)
		}
	}
}

// The reason D2 exists, and it is not the typo. A launcher on a prod floor asserting
// `environment: dev` would walk past a prod freeze window — an authorization defect that typing the
// string would have left completely intact, because `dev` is a perfectly valid environment name.
func TestAssertingAnEnvironmentIsRefused(t *testing.T) {
	err := ValidateChangeContext(map[string]any{types.ChangeContextEnvironmentKey: "dev"})
	if err == nil {
		t.Fatal("a launcher must not be able to choose its own policy environment")
	}
	if !strings.Contains(err.Error(), "property of the floor") {
		t.Errorf("the refusal must say where the environment comes from instead: %v", err)
	}
}

// The reserved namespace: a derived fact a launcher could assert is not a gate, it is a suggestion.
// Same guard shape ADR-0120 uses to keep `stratt.intent/` core's own in Action params.
func TestAssertingACoreOwnedLabelIsRefused(t *testing.T) {
	for _, k := range []string{types.ChangeLabelPrivileged, types.ChangeLabelPrefix + "anything"} {
		err := ValidateChangeContext(map[string]any{k: "false"})
		if err == nil {
			t.Errorf("%q is core's to derive and must be refused from a launcher", k)
			continue
		}
		if !strings.Contains(err.Error(), types.ChangeLabelPrefix) {
			t.Errorf("the refusal must name the reserved namespace: %v", err)
		}
	}
}

// It must stay QUIET on everything else, or it becomes a thing operators route around. Ordinary
// labels, committers, and an unstated class all pass.
func TestOrdinaryChangeContextIsAdmitted(t *testing.T) {
	ok := map[string]any{
		types.ChangeContextClassKey:      types.ChangeClassEmergency,
		types.ChangeContextCommittersKey: []any{"alice", "bob"},
		"incident":                       "INC-42",
		"reasonCode":                     "sev1",
		"team":                           "sre",
		"stratt.dev/note":                "a similar but distinct namespace",
	}
	if err := ValidateChangeContext(ok); err != nil {
		t.Fatalf("an ordinary change context must be admitted: %v", err)
	}
	if err := ValidateChangeContext(nil); err != nil {
		t.Fatalf("no context at all is legal: %v", err)
	}
}

// DeriveElevation is what closes ADR-0117 D1. Core reads a DECLARED path and a boolean; it never
// learns the word `become`, which is the difference between this and the `if ansible{}` that ADR
// refused to write.
func TestDeriveElevationFromADeclaredPath(t *testing.T) {
	elevatedBy := map[string][]string{"ansible": {"become.enabled"}}

	got := DeriveElevation([]types.Step{
		{Name: "a", Actuator: "ansible", Params: map[string]any{"play": "- hosts: all"}},
		{Name: "b", Actuator: "ansible", Params: map[string]any{
			"play": "- hosts: all", "become": map[string]any{"enabled": true},
		}},
	}, elevatedBy)
	if got[types.ChangeLabelPrivileged] != "true" {
		t.Fatalf("a Step whose declared elevating input is set must earn the label: %#v", got)
	}

	// No elevating Step ⇒ no label. An absent label is what lets a Control distinguish "this Run
	// does not escalate" from "this Run escalates"; a label always present with a false value
	// would make the gate depend on reading the value correctly.
	if got := DeriveElevation([]types.Step{
		{Name: "a", Actuator: "ansible", Params: map[string]any{"play": "x"}},
	}, elevatedBy); len(got) != 0 {
		t.Errorf("a Run that escalates nothing must earn no label: %#v", got)
	}

	// An Actuator that declares no elevating inputs derives nothing, even with a `become` in its
	// params. That is the content-blindness working: core has no opinion about the field name.
	if got := DeriveElevation([]types.Step{
		{Name: "a", Actuator: "script", Params: map[string]any{"become": map[string]any{"enabled": true}}},
	}, elevatedBy); len(got) != 0 {
		t.Errorf("core must derive nothing from a field no Actuator declared as elevating: %#v", got)
	}
}

// Truthiness is deliberately narrow. A gate that fires on values the plugin never meant as an
// enable gets routed around, so only a real bool or the string "true" counts.
func TestElevationTruthinessIsNarrow(t *testing.T) {
	elevatedBy := map[string][]string{"ansible": {"become.enabled"}}
	for name, v := range map[string]any{
		"false bool":   false,
		"false string": "false",
		"zero":         0,
		"one":          1,
		"empty string": "",
		"nil":          nil,
		"a map":        map[string]any{"x": 1},
	} {
		got := DeriveElevation([]types.Step{
			{Name: "a", Actuator: "ansible", Params: map[string]any{"become": map[string]any{"enabled": v}}},
		}, elevatedBy)
		if len(got) != 0 {
			t.Errorf("%s must not read as elevation: %#v", name, got)
		}
	}
	// And the two that do.
	for _, v := range []any{true, "true"} {
		got := DeriveElevation([]types.Step{
			{Name: "a", Actuator: "ansible", Params: map[string]any{"become": map[string]any{"enabled": v}}},
		}, elevatedBy)
		if got[types.ChangeLabelPrivileged] != "true" {
			t.Errorf("%#v must read as elevation", v)
		}
	}
}

// A path that does not resolve must not panic or half-match: an Actuator may declare a path a given
// Step simply does not use.
func TestElevationPathThatDoesNotResolve(t *testing.T) {
	elevatedBy := map[string][]string{"ansible": {"become.enabled", "a.b.c.d"}}
	for name, params := range map[string]map[string]any{
		"nil params":            nil,
		"wrong shape":           {"become": "yes"},
		"partial path":          {"become": map[string]any{}},
		"leaf where map wanted": {"a": map[string]any{"b": "not-a-map"}},
	} {
		if got := DeriveElevation([]types.Step{{Name: "s", Actuator: "ansible", Params: params}}, elevatedBy); len(got) != 0 {
			t.Errorf("%s must derive nothing: %#v", name, got)
		}
	}
}
