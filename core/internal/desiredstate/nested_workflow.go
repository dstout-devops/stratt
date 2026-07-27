package desiredstate

import (
	"fmt"
	"sort"
	"strings"

	"slices"

	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/template"
	"github.com/dstout-devops/stratt/types"
)

// ── nested Workflow Steps (ADR-0139) ─────────────────────────────────────────
//
// Charter §2.3 has listed nesting in the definition of a Workflow since day one, and ADR-0011
// deferred it explicitly rather than dropping it. Everything else on that deferral list has
// shipped; this is the remainder.
//
// Everything checkable about a nested edge is checkable in the ESTATE, because Workflow→Workflow
// edges form a static graph in Git. A cycle, a missing child, or inputs the child's schema refuses
// are all diff-time facts, and discovering any of them at launch means discovering them inside a
// half-run tree.

// maxNestingDepth caps how deep the Workflow→Workflow graph may go.
//
// Unbounded nesting is unbounded Temporal child depth, and the failure mode is a floor that stops
// accepting work for reasons no declaration explains. The number is deliberately small: nesting
// exists so a Workflow can compose provision-then-converge, not so an estate can encode a call
// stack. Raising it is a decision, which is why it is a named constant and not a literal.
const maxNestingDepth = 5

// checkNestedWorkflows validates every nested Step in the tree: the child exists, its declared
// inputs are satisfied, no cycle is reachable, and the depth cap holds.
func checkNestedWorkflows(decls Declarations) error {
	byName := make(map[string]types.Workflow, len(decls.Workflows))
	for _, w := range decls.Workflows {
		byName[w.Name] = w
	}
	for _, w := range decls.Workflows {
		for _, s := range w.Steps {
			what := fmt.Sprintf("workflow %s: step %s", w.Name, s.Name)
			for _, childName := range nestedTargets(decls, s) {
				child, ok := byName[childName]
				if !ok {
					return fmt.Errorf("%s runs workflow %q, which is not declared — the Step would fail at "+
						"launch inside a half-run tree, so it is refused here (§1.8)", what, childName)
				}
				// The child's own launch interface, applied at the moment the reference is
				// written. RunDAG re-applies it at launch through ResolveLaunchInputs (the one
				// chokepoint, ADR-0118 D4); this is that same rule moved to the diff.
				//
				// For the CLASS form this runs once per CANDIDATE (D4). Which provider wins
				// depends on runtime state Git cannot see, so inputs that fit only one of them
				// break the other on a capability-binding change — the moment nobody is looking.
				if err := checkNestedInputs(what, child, s.Inputs); err != nil {
					return err
				}
			}
			if s.WorkflowCapability != "" && len(nestedTargets(decls, s)) == 0 {
				return fmt.Errorf("%s: no declared provider of capability %q builds Intent/%s — the Step "+
					"would resolve to nothing at launch, inside a half-run tree (§1.8). Declare a provider "+
					"whose provisions cover %s, or name the Workflow directly",
					what, s.WorkflowCapability, s.ForKind, s.ForKind)
			}
		}
	}
	// Cycles and depth are properties of the whole edge set, not of one Step.
	return checkNestingGraph(decls, byName)
}

// nestedTargets lists every Workflow a nested Step could run: exactly one for the concrete form,
// and one per CANDIDATE PROVIDER for the class form (ADR-0139 D4/D5).
//
// Deliberately NOT filtered by verification or environment — the same reasoning
// remediationCandidates records: this runs over a Git tree where neither is knowable, and a
// narrower set here would be a weaker check for no benefit.
//
// Only the `provisions` map is searched. `remediates` and `decommissions` are not, and cannot be
// without a verb this form does not carry: vcenter maps `Compute` in BOTH provisions and
// decommissions, so searching across maps would be ambiguous for the most ordinary provider in the
// estate — and choosing between "build" and "tear down" is not a tiebreak core may make (§2.4).
func nestedTargets(decls Declarations, s types.Step) []string {
	if s.Workflow != "" {
		return []string{s.Workflow}
	}
	if s.WorkflowCapability == "" || s.ForKind == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(provides []string, provisions map[string]string) {
		if !slices.Contains(provides, s.WorkflowCapability) {
			return
		}
		if wf := provisions[s.ForKind]; wf != "" && !seen[wf] {
			seen[wf] = true
			out = append(out, wf)
		}
	}
	for _, a := range decls.Actuators {
		add(a.Provides, a.Provisions)
	}
	for _, c := range decls.Connectors {
		add(c.Provides, c.Provisions)
	}
	sort.Strings(out)
	return out
}

// checkNestedInputs validates a nested Step's arguments against the child's declared interface.
//
// NAMES and REQUIRED are checked always — they are static facts about two declarations, true
// whatever a template resolves to. VALUES are checked only when nothing is templated, exactly as
// every other params validator in this package defers them: a {{.launch.x}} binding is not knowable
// until launch, and refusing it here would break the parameterised case entirely.
func checkNestedInputs(what string, child types.Workflow, inputs map[string]any) error {
	declared, err := contract.InputNames(child.Inputs)
	if err != nil {
		return fmt.Errorf("%s: workflow %q inputs schema: %w", what, child.Name, err)
	}
	names := make([]string, 0, len(declared))
	for n := range declared {
		names = append(names, n)
	}
	sort.Strings(names)
	for k := range inputs {
		if !declared[k] {
			return fmt.Errorf("%s: %q is not a declared input of workflow %s (declared: %v)",
				what, k, child.Name, names)
		}
	}
	required, err := contract.RequiredNames(child.Inputs)
	if err != nil {
		return fmt.Errorf("%s: workflow %q inputs schema: %w", what, child.Name, err)
	}
	for _, req := range required {
		if _, ok := inputs[req]; !ok {
			return fmt.Errorf("%s: workflow %s requires input %q, which this Step does not pass",
				what, child.Name, req)
		}
	}
	if template.Has(inputs) {
		return nil
	}
	if _, err := contract.ResolveLaunchInputs(child.Name, child.Inputs, inputs); err != nil {
		return fmt.Errorf("%s: inputs do not satisfy workflow %s's interface: %w", what, child.Name, err)
	}
	return nil
}

// checkNestingGraph refuses a cycle in the Workflow→Workflow edges, naming the ring, and enforces
// the depth cap. One DFS answers both: the recursion stack IS the path, so a repeat is a cycle and
// a long stack is excess depth.
func checkNestingGraph(decls Declarations, byName map[string]types.Workflow) error {
	const (
		unvisited = 0
		onStack   = 1
		done      = 2
	)
	state := make(map[string]int, len(byName))
	var path []string

	var walk func(name string) error
	walk = func(name string) error {
		switch state[name] {
		case onStack:
			// Name the RING, not just the fact. A cycle diagnostic that does not show the loop
			// leaves the reader to reconstruct it from a tree of files.
			at := 0
			for i, n := range path {
				if n == name {
					at = i
					break
				}
			}
			return fmt.Errorf("nested workflow cycle: %s → %s — a Workflow that reaches itself never "+
				"terminates, and the depth cap would surface it as an unexplained failure mid-tree (§2.4/ADR-0139 D5)",
				strings.Join(path[at:], " → "), name)
		case done:
			return nil
		}
		state[name] = onStack
		path = append(path, name)
		if len(path) > maxNestingDepth {
			return fmt.Errorf("nested workflow depth %d exceeds the cap of %d: %s — unbounded nesting is "+
				"unbounded Temporal child depth, whose failure mode is a floor that stops accepting work "+
				"for reasons no declaration explains (ADR-0139 D5)",
				len(path), maxNestingDepth, strings.Join(path, " → "))
		}
		w := byName[name]
		// Deterministic order so the same estate always reports the same ring.
		// The CLASS form's edge set is EVERY candidate, not the currently-bound provider: a
		// cycle that appears only after a capability-binding change is still a cycle, and it
		// would surface as an unexplained depth failure mid-tree.
		children := make([]string, 0, len(w.Steps))
		for _, s := range w.Steps {
			children = append(children, nestedTargets(decls, s)...)
		}
		sort.Strings(children)
		for _, c := range children {
			if _, ok := byName[c]; !ok {
				continue // already refused above with a better message
			}
			if err := walk(c); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		state[name] = done
		return nil
	}

	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := walk(n); err != nil {
			return err
		}
	}
	return nil
}
