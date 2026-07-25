package compiler

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func TestDiffIDs(t *testing.T) {
	joins, leaves := diffIDs([]string{"a", "b", "c"}, []string{"b", "c", "d", "e"})
	if !reflect.DeepEqual(joins, []string{"d", "e"}) {
		t.Fatalf("joins: %v", joins)
	}
	if !reflect.DeepEqual(leaves, []string{"a"}) {
		t.Fatalf("leaves: %v", leaves)
	}
	// No previous set: everything joins, nothing leaves.
	joins, leaves = diffIDs(nil, []string{"x", "y"})
	if len(joins) != 2 || len(leaves) != 0 {
		t.Fatalf("first compile: joins=%v leaves=%v", joins, leaves)
	}
}

func TestExceedsDelta(t *testing.T) {
	if exceedsDelta(0, 100, 0.5) {
		t.Fatal("empty previous set can never exceed (first compile is free)")
	}
	if !exceedsDelta(10, 6, 0.5) {
		t.Fatal("6 of 10 changed must exceed 0.5")
	}
	if exceedsDelta(10, 5, 0.5) {
		t.Fatal("5 of 10 is not > 0.5")
	}
}

func TestSubstituteExpectation(t *testing.T) {
	spec := map[string]any{"package": "google-chrome"}
	exp := types.FacetExpectation{
		Namespace: "apps.installed",
		Contains:  json.RawMessage(`"{{.spec.package}}"`),
	}
	got, serr := substituteExpectation(exp, spec)
	if serr != "" {
		t.Fatal(serr)
	}
	if string(got.Contains) != `"google-chrome"` {
		t.Fatalf("contains substitution: %s", got.Contains)
	}
	// A literal equals (no template) passes through untouched.
	exp = types.FacetExpectation{Namespace: "os.kernel", Path: "arch", Equals: json.RawMessage(`"x86_64"`)}
	got, serr = substituteExpectation(exp, spec)
	if serr != "" || string(got.Equals) != `"x86_64"` {
		t.Fatalf("literal equals: %s %q", got.Equals, serr)
	}
	// Missing equals AND contains is an error.
	if _, serr := substituteExpectation(types.FacetExpectation{Namespace: "x"}, spec); serr == "" {
		t.Fatal("expectation without equals/contains must error")
	}
}

func TestDetectClaimConflicts(t *testing.T) {
	claims := []claimRecord{
		{"apps.installed", "e1", types.ClaimExclusive, "asgA"},
		{"apps.installed", "e1", types.ClaimExclusive, "asgB"}, // conflict on (ns,e1)
		{"apps.installed", "e2", types.ClaimExclusive, "asgA"}, // asgA alone on e2: ok
		{"trust.store", "e1", types.ClaimAdditive, "asgC"},
		{"trust.store", "e1", types.ClaimAdditive, "asgD"}, // additive: no conflict
	}
	poisoned := detectClaimConflicts(claims, map[string]bool{})
	if len(poisoned) != 2 {
		t.Fatalf("want asgA+asgB poisoned, got %+v", poisoned)
	}
	names := []string{poisoned[0].assignment, poisoned[1].assignment}
	if !reflect.DeepEqual(names, []string{"asgA", "asgB"}) {
		t.Fatalf("poisoned set: %v", names)
	}

	// A skipped assignment's claims are ignored — no phantom conflict.
	poisoned = detectClaimConflicts(claims, map[string]bool{"asgB": true})
	if len(poisoned) != 0 {
		t.Fatalf("skipping one claimant must clear the conflict, got %+v", poisoned)
	}
}

func TestCompiledNameDeterministic(t *testing.T) {
	a := CompiledName("kiosks", "application", 3, 0)
	b := CompiledName("kiosks", "application", 3, 0)
	if a != b || a != "compiled-kiosks-application-v3-r0" {
		t.Fatalf("name: %q", a)
	}
}

// TestValidateResolvedSpecRejectsAnIncompleteSpec is the OTHER HALF of a validation
// change ADR-0118 D1 made, and it exists so that change cannot become a silent
// relaxation.
//
// Declaration-time Intent validation moved from COMPLETE to PARTIAL, because with values
// spread across Blueprint defaults, the Intent and the Assignment, an Intent legitimately
// omits a field it leaves to its Assignment — so `spec: {}` for a kind with required
// fields must now PARSE. That is only defensible if completeness is enforced somewhere,
// and this is that somewhere: the merged spec, at compile.
//
// The case is the one that used to be caught at parse (desiredstate's intent_test.go
// rejection table): Intent/Certificate requires issuer + commonName + renewBefore.
func TestValidateResolvedSpecRejectsAnIncompleteSpec(t *testing.T) {
	if err := validateResolvedSpec("Intent/Certificate", map[string]any{}); err == nil {
		t.Fatal("an empty resolved spec must fail its kind's required fields at compile")
	}
	// A partial merge is still incomplete: two of three required fields is not enough.
	partial := map[string]any{"issuer": "cert-issuer/stratt-dev", "commonName": "web.test"}
	if err := validateResolvedSpec("Intent/Certificate", partial); err == nil {
		t.Fatal("a resolved spec missing renewBefore must fail at compile")
	}
}

// TestValidateResolvedSpecAcceptsWhatTheLayersCompleted is the case the whole change
// exists to allow: no single layer is a complete spec, but the MERGE is — so an Intent may
// leave renewBefore to its Assignment's values without either declaration being invalid.
func TestValidateResolvedSpecAcceptsWhatTheLayersCompleted(t *testing.T) {
	resolved := map[string]any{
		"issuer":      "cert-issuer/stratt-dev", // from the Intent
		"commonName":  "web.test",               // from the Intent
		"renewBefore": "360h",                   // from the Assignment's values
	}
	if err := validateResolvedSpec("Intent/Certificate", resolved); err != nil {
		t.Fatalf("a spec completed ACROSS layers must validate: %v", err)
	}
}

// TestValidateResolvedSpecStillTypesPresentFields: relaxing declaration to partial must
// not become "anything goes" — a present field with the wrong type still fails, and
// additionalProperties: false still bites.
func TestValidateResolvedSpecStillTypesPresentFields(t *testing.T) {
	wrongType := map[string]any{"issuer": 7, "commonName": "web.test", "renewBefore": "360h"}
	if err := validateResolvedSpec("Intent/Certificate", wrongType); err == nil {
		t.Fatal("a wrongly-typed field must fail even though the spec is complete")
	}
	unknown := map[string]any{
		"issuer": "cert-issuer/x", "commonName": "web.test", "renewBefore": "360h", "sneaky": true,
	}
	if err := validateResolvedSpec("Intent/Certificate", unknown); err == nil {
		t.Fatal("additionalProperties: false must still reject an unknown field")
	}
}

// TestValidateResolvedSpecRejectsAnUnschemadKind: reaching compile with a kind that has no
// schema means the registry changed under a live Assignment, which must not compile
// silently (§1.8).
func TestValidateResolvedSpecRejectsAnUnschemadKind(t *testing.T) {
	if err := validateResolvedSpec("Intent/Config", map[string]any{}); err == nil {
		t.Fatal("a kind with no spec schema must not validate silently")
	}
}
