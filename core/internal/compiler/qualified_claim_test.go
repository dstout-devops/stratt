package compiler

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// The claim grain is (namespace, qualifier, entity) — so two applications on one host coexist.
//
// THIS IS THE POINT OF ADR-0152. Before it, `app.config` was exclusive at (namespace, entity), so
// apache and tomcat converging one host was a double-claim the compiler refused. Correct under the
// old grain and a real product limit: a host running apache on :80 and tomcat on :8080 is ordinary,
// and the reference estate paid for the limit with a whole second managed node.
//
// What must NOT change is the refusal itself. Two claimants on the SAME qualifier still fail, still
// name every claimant, and there is still no precedence field anywhere (§2.4, the anti-GPO axiom).
// The grain narrowed; the axiom did not move.
func TestTwoQualifiersOnOneEntityDoNotConflict(t *testing.T) {
	claims := []claimRecord{
		{"app.config", "apache", "web-02", types.ClaimExclusive, "apache-tier"},
		{"app.config", "tomcat", "web-02", types.ClaimExclusive, "tomcat-tier"},
	}
	if poisoned := detectClaimConflicts(claims, map[string]bool{}); len(poisoned) != 0 {
		t.Fatalf("apache and tomcat claim DIFFERENT qualifiers of app.config on one host — two facts, "+
			"not two claimants on one. Nothing should be poisoned; got %+v", poisoned)
	}
}

func TestOneQualifierClaimedTwiceStillFails(t *testing.T) {
	claims := []claimRecord{
		{"app.config", "tomcat", "web-02", types.ClaimExclusive, "tomcat-tier"},
		{"app.config", "tomcat", "web-02", types.ClaimExclusive, "tomcat-tier-copy"},
	}
	poisoned := detectClaimConflicts(claims, map[string]bool{})
	if len(poisoned) != 2 {
		t.Fatalf("two exclusive claims on the SAME (namespace, qualifier, entity) must still fail the "+
			"compile naming both (§2.4); got %+v", poisoned)
	}
	// §1.8 was the whole motivation: the old message said "two Blueprints claim app.config" when the
	// true statement names WHICH application on WHICH host.
	if !strings.Contains(poisoned[0].message, `qualified "tomcat"`) {
		t.Errorf("the conflict message must name the qualifier, got: %s", poisoned[0].message)
	}
	if !strings.Contains(poisoned[0].message, "tomcat-tier-copy") {
		t.Errorf("the conflict message must still name every claimant, got: %s", poisoned[0].message)
	}
}

// An UNQUALIFIED conflict message reads exactly as it always did.
//
// Almost every claim in any estate is unqualified, so the new dimension must be invisible where it
// is not load-bearing. A message that grew `qualified ""` on every existing conflict would be a
// regression in the diagnosis this ADR is supposed to improve.
func TestAnUnqualifiedConflictMessageIsUnchanged(t *testing.T) {
	claims := []claimRecord{
		{"apps.installed", "", "e1", types.ClaimExclusive, "asgA"},
		{"apps.installed", "", "e1", types.ClaimExclusive, "asgB"},
	}
	poisoned := detectClaimConflicts(claims, map[string]bool{})
	if len(poisoned) != 2 {
		t.Fatalf("want both poisoned, got %+v", poisoned)
	}
	if strings.Contains(poisoned[0].message, "qualified") {
		t.Errorf("an unqualified conflict must not mention a qualifier, got: %s", poisoned[0].message)
	}
	if !strings.Contains(poisoned[0].message, `exclusive claim conflict on facet "apps.installed" for entity e1`) {
		t.Errorf("the unqualified message text must be unchanged, got: %s", poisoned[0].message)
	}
}

// A qualifier is DERIVED from the resolved spec at compile — the same engine every other value on
// the expectation uses (ADR-0152 D3, ADR-0024).
func TestTheQualifierResolvesFromTheSpec(t *testing.T) {
	exp := types.FacetExpectation{
		Namespace: "app.config",
		Qualifier: "{{.spec.package}}",
		Path:      "port",
		Equals:    []byte(`"8080"`),
	}
	got, serr := substituteExpectation(exp, map[string]any{"package": "tomcat", "port": "8080"})
	if serr != "" {
		t.Fatalf("substitute: %s", serr)
	}
	if got.Qualifier != "tomcat" {
		t.Errorf("qualifier = %q, want tomcat", got.Qualifier)
	}
}

// A qualifier that is PRESENT and resolves to empty is a COMPILE ERROR, not a quiet unqualified
// claim.
//
// Absent and empty must not render the same. `{{.spec.pakcage}}`, or a spec field this Assignment
// never set, would otherwise leave the route compiling fine while its exclusivity silently widened
// from (entity, app.config, apache) back to (entity, app.config) — the grain of an exclusive claim
// moving by accident, which is the same §2.4 surprise the design refuses to inflict deliberately
// when it declines to default the qualifier at all.
func TestAQualifierResolvingToEmptyIsRefused(t *testing.T) {
	exp := types.FacetExpectation{
		Namespace: "app.config",
		Qualifier: "{{.spec.package}}",
		Path:      "port",
		Equals:    []byte(`"8080"`),
	}
	_, serr := substituteExpectation(exp, map[string]any{"package": "", "port": "8080"})
	if serr == "" {
		t.Fatal("a declared qualifier resolving to empty must fail the compile — silently returning to " +
			"the unqualified grain is how an exclusive claim's blast radius changes without review")
	}
	if !strings.Contains(serr, "unqualified claim grain") {
		t.Errorf("the refusal must say what the empty resolution would have COST, got: %s", serr)
	}
}
