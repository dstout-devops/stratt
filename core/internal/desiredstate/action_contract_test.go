package desiredstate

import (
	"strings"
	"testing"
)

// An Action Step's Contract must EXIST at load, even when its params cannot be
// checked yet (ADR-0031, §2.2 — "an uncontracted operation must not exist").
//
// Existence is a static fact about the estate: it does not depend on what a template
// resolves to. Deferring it alongside the VALUES meant a Step naming an uncontracted
// Action passed the load and failed at launch with "no input contract for action" —
// admitted where a human is reading a diff, fatal somewhere they are not (§1.8).

// actionStepEstate writes an estate with one Action Step using the given action and
// params body (raw YAML for the params block).
func actionStepEstate(t *testing.T, action, paramsYAML string) string {
	t.Helper()
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "workflows", "w.yaml",
		"name: wf\n"+
			"inputs:\n  type: object\n  additionalProperties: false\n"+
			"  properties:\n    value: {type: string}\n    name: {type: string}\n"+
			"steps:\n  - name: go\n    action: "+action+"\n    params:\n"+paramsYAML)
	return root
}

// TestUncontractedActionIsRefusedEvenWithTemplatedParams is the gap this closes. The
// templated form is the one that mattered: concrete params already failed, because
// validation ran and found no Contract to validate against. Templated params returned
// early and skipped the existence check with them.
func TestUncontractedActionIsRefusedEvenWithTemplatedParams(t *testing.T) {
	root := actionStepEstate(t, "nosuch/operation", "      key: \"{{.launch.value}}\"\n")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("a Step naming an Action with no input Contract must be refused at load — deferring it to " +
			"launch means the failure lands where nobody is reading a diff")
	}
	if !strings.Contains(err.Error(), "no input contract") {
		t.Fatalf("the diagnostic must name the missing Contract: %v", err)
	}
}

// TestUncontractedActionIsRefusedWithConcreteParams: the case that already worked,
// kept so a refactor cannot quietly move the check behind the template guard again.
func TestUncontractedActionIsRefusedWithConcreteParams(t *testing.T) {
	root := actionStepEstate(t, "nosuch/operation", "      key: value\n")
	if _, err := ParseDir(root, nil); err == nil {
		t.Fatal("an uncontracted Action with concrete params must stay refused")
	}
}

// TestContractedActionWithTemplatedParamsStillLoads is the counterweight, and it is the
// reason the early return exists at all: a real Action whose params are only knowable at
// launch must still load. Break this and every {{.launch.x}}-parameterised Step in the
// estate fails, which is a far worse outcome than the hole being closed.
func TestContractedActionWithTemplatedParamsStillLoads(t *testing.T) {
	// netbox/ipam-resolve ships input+output contracts and is invoked with templated
	// params by the shipped vsphere-subnet-build Workflow.
	// cert-issuer/create-intermediate is CORE-SHIPPED and stays so: `cert-issuer` is a NEUTRAL
	// Actuator name (§1.5 — a step-ca plugin could implement it), which makes its contracts a
	// SEAM rather than one vendor's self contract (ADR-0138 D3). netbox's used to serve here and
	// no longer can: since the relocation, an estate that admits no plugins holds no plugin
	// contracts — the authority model working, not a regression.
	root := actionStepEstate(t, "cert-issuer/create-intermediate", "      commonName: \"{{.launch.name}}\"\n")
	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("a contracted Action with templated params must still load — its VALUES are checked at launch: %v", err)
	}
}
