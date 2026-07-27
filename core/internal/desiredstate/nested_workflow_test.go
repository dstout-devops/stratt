package desiredstate

import (
	"strings"
	"testing"
)

// A Step may run another declared Workflow (charter §2.3 "…convergence, nesting"; ADR-0011
// deferred it, ADR-0139 resumes it). Workflow→Workflow edges form a STATIC graph in Git, so every
// checkable property is a diff-time fact — discovering any of them at launch means discovering it
// inside a half-run tree.

// nestedEstate writes a parent Workflow whose one Step runs `child`, plus a child declaring
// inputs {name (required), size}.
func nestedEstate(t *testing.T, parentStep string) string {
	t.Helper()
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "workflows", "child.yaml",
		"name: build\n"+
			"inputs:\n  type: object\n  additionalProperties: false\n  required: [name]\n"+
			"  properties:\n    name: {type: string}\n    size: {type: integer}\n"+
			"steps:\n  - name: go\n    viewName: hosts\n    actuator: script\n    params: {script: 'echo hi'}\n")
	writeKind(t, root, "workflows", "parent.yaml",
		"name: onboard\n"+
			"inputs:\n  type: object\n  additionalProperties: false\n"+
			"  properties:\n    host: {type: string}\n"+
			"steps:\n"+parentStep)
	return root
}

func TestNestedWorkflowStepLoads(t *testing.T) {
	root := nestedEstate(t, "  - name: provision\n    workflow: build\n    inputs:\n      name: web-01\n      size: 2\n")
	got, err := ParseDir(root, nil)
	if err != nil {
		t.Fatalf("a Step running a declared Workflow with satisfying inputs must load: %v", err)
	}
	var found bool
	for _, w := range got.Workflows {
		for _, s := range w.Steps {
			if s.Workflow == "build" {
				found = true
				if s.Inputs["name"] != "web-01" {
					t.Errorf("the Step's inputs must survive the parse: %v", s.Inputs)
				}
			}
		}
	}
	if !found {
		t.Fatal("the nested Step must survive the parse")
	}
}

// The child's own launch interface, applied where the reference is WRITTEN. RunDAG re-applies it
// at launch through the one chokepoint; this is that rule moved to the diff.
func TestNestedStepMissingRequiredInputIsRefused(t *testing.T) {
	root := nestedEstate(t, "  - name: provision\n    workflow: build\n    inputs:\n      size: 2\n")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("a nested Step omitting the child's required input must be refused")
	}
	if !strings.Contains(err.Error(), "requires input \"name\"") {
		t.Fatalf("the diagnostic must name the missing input: %v", err)
	}
}

func TestNestedStepUndeclaredInputIsRefused(t *testing.T) {
	root := nestedEstate(t, "  - name: provision\n    workflow: build\n    inputs:\n      name: web-01\n      nosuch: x\n")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("an input the child does not declare must be refused — the child's schema closes itself, " +
			"so it would be rejected at launch instead")
	}
	if !strings.Contains(err.Error(), "nosuch") {
		t.Fatalf("the diagnostic must name the offending key: %v", err)
	}
}

func TestNestedStepUnknownWorkflowIsRefused(t *testing.T) {
	root := nestedEstate(t, "  - name: provision\n    workflow: nosuchflow\n")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("a Step running an undeclared Workflow must be refused at load")
	}
	if !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("the diagnostic must say the child is missing: %v", err)
	}
}

// Templated inputs must still LOAD — their values are a launch-time fact — but their NAMES are
// checked, because which keys the child declares is static whatever a template resolves to.
func TestNestedStepTemplatedInputsLoadButNamesAreChecked(t *testing.T) {
	ok := nestedEstate(t, "  - name: provision\n    workflow: build\n    inputs:\n      name: \"{{.launch.host}}\"\n")
	if _, err := ParseDir(ok, nil); err != nil {
		t.Fatalf("a templated input must still load — its value is not knowable until launch: %v", err)
	}
	bad := nestedEstate(t, "  - name: provision\n    workflow: build\n    inputs:\n      name: \"{{.launch.host}}\"\n      nosuch: \"{{.launch.host}}\"\n")
	if _, err := ParseDir(bad, nil); err == nil {
		t.Fatal("a templated value does not excuse an undeclared input NAME")
	}
}

// A nested Step is a fifth SHAPE, not a modifier. Combining it gives the Step two things to run
// and no rule to choose (§2.4).
func TestNestedStepIsExclusiveWithOtherShapes(t *testing.T) {
	root := nestedEstate(t, "  - name: provision\n    workflow: build\n    inputs: {name: x}\n    viewName: hosts\n    actuator: script\n")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("a nested Step that is also an actuation must be refused")
	}
	if !strings.Contains(err.Error(), "not also a gate") {
		t.Fatalf("the diagnostic must say the shapes are exclusive: %v", err)
	}
}

// `inputs` on a non-nested Step is a field nothing reads — the half-declaration defect this
// package refuses everywhere else.
func TestInputsOnANonNestedStepIsRefused(t *testing.T) {
	root := nestedEstate(t, "  - name: go\n    viewName: hosts\n    actuator: script\n    params: {script: hi}\n    inputs: {name: x}\n")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("inputs on an actuation Step must be refused")
	}
	if !strings.Contains(err.Error(), "only meaningful on a nested") {
		t.Fatalf("the diagnostic must point at `params`: %v", err)
	}
}

// A Workflow that reaches itself never terminates, and the depth cap would surface it as an
// unexplained failure mid-tree. The diagnostic must show the RING, not just the fact.
func TestNestedWorkflowCycleIsRefused(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "workflows", "a.yaml", "name: a\nsteps:\n  - name: s\n    workflow: b\n")
	writeKind(t, root, "workflows", "b.yaml", "name: b\nsteps:\n  - name: s\n    workflow: a\n")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("a Workflow→Workflow cycle must be refused at load")
	}
	if !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "a → b → a") {
		t.Fatalf("the diagnostic must name the ring so the reader need not reconstruct it: %v", err)
	}
}

func TestNestedWorkflowSelfReferenceIsRefused(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "workflows", "a.yaml", "name: a\nsteps:\n  - name: s\n    workflow: a\n")
	if _, err := ParseDir(root, nil); err == nil {
		t.Fatal("a Workflow nesting itself must be refused")
	}
}

// Unbounded nesting is unbounded Temporal child depth, whose failure mode is a floor that stops
// accepting work for reasons no declaration explains.
func TestNestedWorkflowDepthCapIsEnforced(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	// A chain one longer than the cap.
	n := maxNestingDepth + 1
	for i := range n {
		body := "name: w" + string(rune('0'+i)) + "\nsteps:\n"
		if i < n-1 {
			body += "  - name: s\n    workflow: w" + string(rune('0'+i+1)) + "\n"
		} else {
			body += "  - name: s\n    viewName: hosts\n    actuator: script\n    params: {script: hi}\n"
		}
		writeKind(t, root, "workflows", "w"+string(rune('0'+i))+".yaml", body)
	}
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatalf("a nesting chain deeper than the cap of %d must be refused", maxNestingDepth)
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Fatalf("the diagnostic must say the cap was exceeded: %v", err)
	}
}
