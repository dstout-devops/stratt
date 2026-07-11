// Package desiredstate reconciles the Git-declared desired state into the
// graph (charter §1.2: desired state lives in Git; drift is the diff). The
// declarable unit in Phase 1 is the View (§2.1: CaC-declared); Intents and
// Assignments join in Phase 2.
//
// The same plan/apply engine serves the API (POST /desired-state/plan|apply,
// used by the stratt CLI) and the in-process reconcile Controller — one
// semantics, two entry points (§1.6).
//
// Phase-2 constraint to carry forward (charter-guardian, §2.1): when
// Assignments land, the compiler must reject an Assignment referencing a
// View that is not cac-declared — otherwise desired state escapes Git.
package desiredstate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// Declaration is one declared View. JSON tags mirror the API's
// ViewDeclaration wire schema so the CLI can send declarations verbatim.
type Declaration struct {
	Name     string             `json:"name"`
	Selector types.ViewSelector `json:"selector"`
}

// Action is what reconciliation will do (or did) to one View.
type Action string

const (
	ActionCreate Action = "create"
	ActionUpdate Action = "update"
	// ActionAdopt promotes an api-declared View into the desired state
	// (ownership transfers to cac; the selector may change in the same step).
	ActionAdopt Action = "adopt"
	ActionNoop  Action = "noop"
	// ActionDelete prunes a cac-declared View absent from the declarations.
	// api-declared Views are never pruned.
	ActionDelete Action = "delete"
)

// PlanEntry is the plan for one View. JSON tags mirror the wire schema.
type PlanEntry struct {
	Name   string `json:"name"`
	Action Action `json:"action"`
	// MemberCount is the live Entity count the relevant selector matches now
	// (the desired selector; for deletes, the outgoing one) — blast-radius
	// visibility before anything executes (§4.3).
	MemberCount int64               `json:"memberCount"`
	OldSelector *types.ViewSelector `json:"oldSelector,omitempty"`
	NewSelector *types.ViewSelector `json:"newSelector,omitempty"`
	// Error carries a per-View apply failure (apply continues past it).
	Error string `json:"error,omitempty"`
}

// Plan is the ordered reconciliation plan.
type Plan struct {
	Entries []PlanEntry `json:"entries"`
}

// Changes reports how many entries are not noops.
func (p Plan) Changes() int {
	n := 0
	for _, e := range p.Entries {
		if e.Action != ActionNoop {
			n++
		}
	}
	return n
}

// PruneStats reports how many currently-cac Views this plan would delete, out
// of how many exist. Every current cac View appears in a plan as exactly one
// of noop/update/delete, so both numbers fall out of the entries.
func (p Plan) PruneStats() (deletes, cacTotal int) {
	for _, e := range p.Entries {
		switch e.Action {
		case ActionDelete:
			deletes++
			cacTotal++
		case ActionNoop, ActionUpdate:
			cacTotal++
		}
	}
	return deletes, cacTotal
}

// ── declarations directory ──────────────────────────────────────────────────

// yaml-side shapes: yaml.v3 does not read json tags, and Equals must become
// canonical JSON for the selector document.
type declFile struct {
	Name     string       `yaml:"name"`
	Selector declSelector `yaml:"selector"`
}
type declSelector struct {
	Kinds  []string          `yaml:"kinds"`
	Labels map[string]string `yaml:"labels"`
	Facets []declFacet       `yaml:"facets"`
}
type declFacet struct {
	Namespace string `yaml:"namespace"`
	Path      string `yaml:"path"`
	Equals    any    `yaml:"equals"`
}

// ParseDir reads every *.yaml/*.yml under <root>/views. A missing views
// directory is an error, not an empty set — an empty set prunes every
// cac-declared View, and a mistyped path must never look like one.
func ParseDir(root string) ([]Declaration, error) {
	dir := filepath.Join(root, "views")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("desiredstate: read declarations: %w", err)
	}
	seen := map[string]string{} // view name → file
	var out []Declaration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("desiredstate: %s: %w", path, err)
		}
		var f declFile
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true) // typos in declarations must fail, not vanish
		if err := dec.Decode(&f); err != nil {
			return nil, fmt.Errorf("desiredstate: %s: %w", path, err)
		}
		if f.Name == "" {
			return nil, fmt.Errorf("desiredstate: %s: name is required", path)
		}
		if prev, dup := seen[f.Name]; dup {
			return nil, fmt.Errorf("desiredstate: view %q declared in both %s and %s", f.Name, prev, path)
		}
		seen[f.Name] = path
		sel, err := f.Selector.toSelector()
		if err != nil {
			return nil, fmt.Errorf("desiredstate: %s: %w", path, err)
		}
		out = append(out, Declaration{Name: f.Name, Selector: sel})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (ds declSelector) toSelector() (types.ViewSelector, error) {
	sel := types.ViewSelector{Kinds: ds.Kinds, Labels: ds.Labels}
	for _, f := range ds.Facets {
		if f.Namespace == "" {
			return sel, fmt.Errorf("facet predicate requires a namespace")
		}
		if f.Equals == nil {
			return sel, fmt.Errorf("facet predicate on %s requires equals", f.Namespace)
		}
		eq, err := json.Marshal(f.Equals)
		if err != nil {
			return sel, fmt.Errorf("facet predicate on %s: %w", f.Namespace, err)
		}
		sel.Facets = append(sel.Facets, types.FacetPredicate{
			Namespace: f.Namespace, Path: f.Path, Equals: json.RawMessage(eq),
		})
	}
	return sel, nil
}

// ── plan / apply ─────────────────────────────────────────────────────────────

// ComputePlan diffs the declarations against the graph's current Views.
func ComputePlan(ctx context.Context, store *graph.Store, decls []Declaration) (Plan, error) {
	cac, err := store.ListViewsDeclaredBy(ctx, graph.DeclaredByCaC)
	if err != nil {
		return Plan{}, err
	}
	api, err := store.ListViewsDeclaredBy(ctx, graph.DeclaredByAPI)
	if err != nil {
		return Plan{}, err
	}
	cacByName := map[string]types.View{}
	for _, v := range cac {
		cacByName[v.Name] = v
	}
	apiByName := map[string]types.View{}
	for _, v := range api {
		apiByName[v.Name] = v
	}

	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Name: d.Name, NewSelector: ptr(d.Selector)}
		switch {
		case !existsIn(cacByName, d.Name) && !existsIn(apiByName, d.Name):
			entry.Action = ActionCreate
		case existsIn(apiByName, d.Name):
			v := apiByName[d.Name]
			entry.Action = ActionAdopt
			entry.OldSelector = ptr(v.Selector)
		default:
			v := cacByName[d.Name]
			if selectorsEqual(v.Selector, d.Selector) {
				entry.Action = ActionNoop
				entry.OldSelector = nil
				entry.NewSelector = nil
			} else {
				entry.Action = ActionUpdate
				entry.OldSelector = ptr(v.Selector)
			}
		}
		if entry.Action != ActionNoop {
			n, err := store.CountSelector(ctx, d.Selector)
			if err != nil {
				return Plan{}, err
			}
			entry.MemberCount = n
		}
		plan.Entries = append(plan.Entries, entry)
	}

	// Prune: cac Views no longer declared. api Views are never candidates.
	for _, v := range cac {
		if declared[v.Name] {
			continue
		}
		n, err := store.CountSelector(ctx, v.Selector)
		if err != nil {
			return Plan{}, err
		}
		plan.Entries = append(plan.Entries, PlanEntry{
			Name: v.Name, Action: ActionDelete, MemberCount: n, OldSelector: ptr(v.Selector),
		})
	}
	sort.Slice(plan.Entries, func(i, j int) bool { return plan.Entries[i].Name < plan.Entries[j].Name })
	return plan, nil
}

// Apply executes the plan for the declarations and returns the realized plan.
// Per-View failures are recorded on their entries; the rest still applies.
func Apply(ctx context.Context, store *graph.Store, decls []Declaration) (Plan, error) {
	plan, err := ComputePlan(ctx, store, decls)
	if err != nil {
		return Plan{}, err
	}
	declByName := map[string]types.ViewSelector{}
	for _, d := range decls {
		declByName[d.Name] = d.Selector
	}
	for i := range plan.Entries {
		e := &plan.Entries[i]
		switch e.Action {
		case ActionNoop:
			continue
		case ActionDelete:
			if err := store.DeleteView(ctx, e.Name, graph.DeclaredByCaC); err != nil {
				e.Error = err.Error()
			}
		default: // create, update, adopt
			if _, err := store.DeclareViewAs(ctx, e.Name, declByName[e.Name], graph.DeclaredByCaC); err != nil {
				e.Error = err.Error()
			}
		}
	}
	return plan, nil
}

func ptr(sel types.ViewSelector) *types.ViewSelector { return &sel }

func existsIn(m map[string]types.View, name string) bool { _, ok := m[name]; return ok }

// selectorsEqual compares selectors semantically: both sides round-trip
// through JSON so raw-message formatting and jsonb normalization differences
// don't read as drift.
func selectorsEqual(a, b types.ViewSelector) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	var va, vb any
	if json.Unmarshal(ja, &va) != nil || json.Unmarshal(jb, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}
