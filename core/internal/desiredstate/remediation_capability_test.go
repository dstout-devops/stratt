package desiredstate

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// The declaration half of ADR-0135 D2/D3: what a route and a provider may say, and which
// half-declarations are refused before anything compiles.

// capEstate writes an estate whose Blueprint route remediates via a capability, with `provider`
// declaring `provides` + `remediates` as given. Empty strings omit the field.
func capEstate(t *testing.T, routeExtra, provides, remediates string) string {
	t.Helper()
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	act := "name: cm\npluginIdentity: ansible\njobCommand: [stratt-ansible]\n"
	if provides != "" {
		act += "provides: [" + provides + "]\n"
	}
	if remediates != "" {
		act += "remediates:\n  " + remediates + "\n"
	}
	writeKind(t, root, "actuators", "cm.yaml", act)
	writeKind(t, root, "workflows", "converge.yaml",
		"name: converge\nsteps:\n  - name: go\n    viewName: hosts\n    actuator: cm\n    params: {}\n")
	writeKind(t, root, "blueprints", "bp.yaml",
		"name: bp\nversion: 1\nfor: Intent/Application\nroutes:\n"+
			"  - observe: {namespace: app.config, path: port, equals: \"8080\"}\n"+
			"    claim: exclusive\n"+routeExtra)
	return root
}

// TestRouteRemediationLegIsExclusive: a route has ONE remediation leg. Both set would need a
// winner, and a silent winner between a named Workflow and a resolved one is exactly the implicit
// precedence §2.4 forbids.
func TestRouteRemediationLegIsExclusive(t *testing.T) {
	root := capEstate(t,
		"    remediationWorkflow: converge\n    remediationCapability: configmgmt\n",
		"configmgmt", "Application: converge")
	_, err := ParseDir(root, nil)
	if err == nil || !strings.Contains(err.Error(), "one remediation leg") {
		t.Fatalf("want a mutual-exclusion refusal, got %v", err)
	}
}

// TestRouteCapabilityMustBeAKnownClass: a plugin never mints a capability's meaning (§1.5). A typo
// caught here says "not a known capability class"; caught at compile it would say "no provider",
// which sends the reader hunting for a provider that was never the problem (§1.8).
func TestRouteCapabilityMustBeAKnownClass(t *testing.T) {
	root := capEstate(t, "    remediationCapability: confgimgmt\n", "configmgmt", "Application: converge")
	_, err := ParseDir(root, nil)
	if err == nil || !strings.Contains(err.Error(), "not a known capability class") {
		t.Fatalf("want an unknown-class refusal naming the typo, got %v", err)
	}
}

// TestRemediatesRequiresProvides is the half-declaration rule (§1.8), the same one facetNamespaces
// and provisions each enforce: a map nothing can resolve through is admitted, never consulted, and
// reported nowhere.
func TestRemediatesRequiresProvides(t *testing.T) {
	root := capEstate(t, "    remediationCapability: configmgmt\n", "", "Application: converge")
	_, err := ParseDir(root, nil)
	if err == nil || !strings.Contains(err.Error(), "remediates is set but provides is empty") {
		t.Fatalf("want a provides refusal, got %v", err)
	}
}

// TestRemediatesRejectsPrefixedKind: the maps key by the BARE kind, matching provisions and the
// binding entries. "Intent/Application" would parse cleanly and then match nothing at resolve time
// — admitted, never consulted, silent.
func TestRemediatesRejectsPrefixedKind(t *testing.T) {
	root := capEstate(t, "    remediationCapability: configmgmt\n", "configmgmt", "Intent/Application: converge")
	_, err := ParseDir(root, nil)
	if err == nil || !strings.Contains(err.Error(), "must omit the Intent/ prefix") {
		t.Fatalf("want a bare-kind refusal, got %v", err)
	}
}

// TestCapabilityRoutedParamsCheckedAgainstEveryCandidate is the §1.8 regression this seam could
// have introduced. A name-routed route has its remediationParams validated at DECLARATION; a
// capability-routed one names no Workflow, so a naive implementation defers the check to compile —
// LATER, for exactly the routes the ADR encourages.
//
// The answer is checkProvisioningBuildInputs' own: check every CANDIDATE, not the winner. Which
// provider wins depends on the environment and on which providers are verified — runtime state Git
// cannot see — so a param set that fits only one of them breaks the other on a binding change.
func TestCapabilityRoutedParamsCheckedAgainstEveryCandidate(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	// Two providers of the same class for the same kind; only ONE declares the input.
	writeKind(t, root, "actuators", "a1.yaml",
		"name: cm-a\npluginIdentity: ansible\njobCommand: [stratt-ansible]\nprovides: [configmgmt]\nremediates:\n  Application: converge-a\n")
	writeKind(t, root, "actuators", "a2.yaml",
		"name: cm-b\npluginIdentity: ansible\njobCommand: [stratt-ansible]\nprovides: [configmgmt]\nremediates:\n  Application: converge-b\n")
	writeKind(t, root, "workflows", "converge-a.yaml",
		"name: converge-a\ninputs:\n  type: object\n  additionalProperties: false\n  properties:\n    port: {type: string}\nsteps:\n  - name: go\n    viewName: hosts\n    actuator: cm-a\n    params: {}\n")
	// converge-b does NOT declare `port`.
	writeKind(t, root, "workflows", "converge-b.yaml",
		"name: converge-b\ninputs:\n  type: object\n  additionalProperties: false\n  properties:\n    other: {type: string}\nsteps:\n  - name: go\n    viewName: hosts\n    actuator: cm-b\n    params: {}\n")
	writeKind(t, root, "blueprints", "bp.yaml",
		"name: bp\nversion: 1\nfor: Intent/Application\nroutes:\n"+
			"  - observe: {namespace: app.config, path: port, equals: \"8080\"}\n"+
			"    claim: exclusive\n    remediationCapability: configmgmt\n"+
			"    remediationParams: {port: \"8080\"}\n")

	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("params fitting only ONE candidate provider must fail at declaration — the other breaks on a binding change, which is the moment nobody is looking")
	}
	if !strings.Contains(err.Error(), "converge-b") {
		t.Fatalf("the diagnostic must name the candidate that does not fit: %v", err)
	}
}

// TestRemediatesRoutesButDoesNotGrant pins the ADR's trap in the one place it can be pinned
// cheaply: `remediates` is absent from the authority surface. facetNamespaces beside it IS a
// ceiling; naming a Workflow here confers nothing, and review must not read it as authority.
func TestRemediatesRoutesButDoesNotGrant(t *testing.T) {
	root := capEstate(t, "    remediationCapability: configmgmt\n", "configmgmt", "Application: converge")
	decls, err := ParseDir(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	var act types.Actuator
	for _, a := range decls.Actuators {
		if a.Name == "cm" {
			act = a
		}
	}
	if len(act.Remediates) != 1 {
		t.Fatalf("remediates did not parse: %+v", act.Remediates)
	}
	if len(act.FacetNamespaces) != 0 {
		t.Errorf("declaring remediates must not confer any facet write scope, got %v", act.FacetNamespaces)
	}
}
