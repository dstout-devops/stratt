package desiredstate

import (
	"strings"
	"testing"
)

// A Step may name a capability CLASS instead of a provider's Action (ADR-0140 D3 row 2). The
// point is that the declaration stops naming a provider: `vsphere-subnet-build` reaching NetBox
// by name is the coupling ADR-0104 D1 forbids downward and nothing enforced upward.
//
// Load-time is where the class form has to be checked, because the resolution happens at LAUNCH:
// if the class is misspelled or uncontracted, the diff is the last place a human sees it before
// the failure lands in a Run (§1.8).

// capStepEstate writes an estate with one Step naming a capability class.
func capStepEstate(t *testing.T, class, paramsYAML string) string {
	t.Helper()
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "workflows", "w.yaml",
		"name: wf\n"+
			"inputs:\n  type: object\n  additionalProperties: false\n"+
			"  properties:\n    name: {type: string}\n"+
			"steps:\n  - name: allocate\n    actionCapability: "+class+"\n    params:\n"+paramsYAML)
	return root
}

// The shape that should load: `ipam` is Action-shaped and its class Contracts ship (ADR-0111).
// Note what is NOT here — no provider name anywhere in the declaration.
func TestActionCapabilityStepLoads(t *testing.T) {
	root := capStepEstate(t, "ipam", "      key: \"{{.launch.name}}\"\n      pool: \"10.0.0.0/8\"\n      size: 24\n")
	got, err := ParseDir(root, nil)
	if err != nil {
		t.Fatalf("a Step naming a contracted, Action-shaped capability class must load: %v", err)
	}
	step := got.Workflows[0].Steps[0]
	if step.ActionCapability != "ipam" {
		t.Fatalf("actionCapability must survive the parse, got %q", step.ActionCapability)
	}
	if step.Action != "" {
		t.Fatalf("the class must NOT be resolved to a provider at load — resolution is a launch-time "+
			"fact, and baking it in would freeze the binding into the estate: got %q", step.Action)
	}
}

// Params are validated against the CLASS Contract, not any provider's. This is what makes the
// Step valid independent of the binding — check it against the wrong document and the declaration's
// validity would change when the provider does.
func TestActionCapabilityParamsAreCheckedAgainstTheClassContract(t *testing.T) {
	// `size` is an integer in capabilities/ipam.input.
	root := capStepEstate(t, "ipam", "      key: web\n      pool: \"10.0.0.0/8\"\n      size: \"twenty-four\"\n")
	if _, err := ParseDir(root, nil); err == nil {
		t.Fatal("params that violate the class Contract must be refused at load")
	}
}

// A class with no Contracts cannot be named by a Step. `provisioning` is the live example and the
// reason this check matters: it is a real, declared capability class fulfilled through a per-kind
// Workflow map (ADR-0140 D3 row 1), so it is not invocable as an Action at all. Admitting the Step
// would produce a declaration that can never launch.
func TestActionCapabilityRefusesANonActionShapedClass(t *testing.T) {
	root := capStepEstate(t, "provisioning", "      key: web\n")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("a class with no input Contract is not Action-shaped and must not be nameable by a Step")
	}
	if !strings.Contains(err.Error(), "has no input contract") {
		t.Fatalf("the diagnostic must name the missing class Contract: %v", err)
	}
}

func TestActionCapabilityRefusesAnUnknownClass(t *testing.T) {
	root := capStepEstate(t, "nosuchclass", "      key: web\n")
	if _, err := ParseDir(root, nil); err == nil {
		t.Fatal("an unknown capability class must be refused at load, not at launch")
	}
}

// Naming both gives the Step two answers to "what runs here", and a rule to pick between them is
// the implicit precedence §2.4 exists to refuse — silently preferring the concrete name would make
// every class-named Step's behaviour depend on whether someone also left an `action:` in place.
func TestActionAndActionCapabilityAreMutuallyExclusive(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "workflows", "w.yaml",
		"name: wf\nsteps:\n  - name: allocate\n    action: netbox/ipam-resolve\n"+
			"    actionCapability: ipam\n    params:\n      key: web\n")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("a Step naming both an action and an actionCapability must be refused")
	}
	if !strings.Contains(err.Error(), "not both") {
		t.Fatalf("the diagnostic must say they are exclusive: %v", err)
	}
}

// A capability Step is still an ACTION step — targetless. Gaining a View/Actuator must fail the
// same way a named-Action step does, or the shape check has a hole the class form walks through.
func TestActionCapabilityIsStillTargetless(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "workflows", "w.yaml",
		"name: wf\nsteps:\n  - name: allocate\n    actionCapability: ipam\n"+
			"    viewName: hosts\n    params:\n      key: web\n")
	if _, err := ParseDir(root, nil); err == nil {
		t.Fatal("a capability Step with a View must be refused — an Action carries no View (ADR-0031)")
	}
}
