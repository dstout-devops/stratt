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

// Declarations is the full declared desired state — every kind the repo can
// declare (Views since slice 2; CredentialRef pointers since ADR-0009;
// Intents/Assignments arrive in Phase 2).
type Declarations struct {
	Views          []Declaration         `json:"views"`
	CredentialRefs []types.CredentialRef `json:"credentialRefs"`
}

// Declared kinds appearing in plans.
const (
	KindView          = "view"
	KindCredentialRef = "credential-ref"
)

// Action is what reconciliation will do (or did) to one declared object.
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

// PlanEntry is the plan for one declared object. JSON tags mirror the wire
// schema.
type PlanEntry struct {
	// Kind is the declared kind: "view" | "credential-ref".
	Kind   string `json:"kind"`
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

// PruneStats reports, per declared kind, how many currently-cac objects this
// plan would delete out of how many exist. Every current cac object appears
// in a plan as exactly one of noop/update/delete, so both numbers fall out
// of the entries. Per-kind so one kind's bulk (e.g. many Views) can never
// mask the total disappearance of another (e.g. every CredentialRef).
func (p Plan) PruneStats() map[string][2]int { // kind → {deletes, cacTotal}
	out := map[string][2]int{}
	for _, e := range p.Entries {
		kind := e.Kind
		if kind == "" {
			kind = KindView
		}
		s := out[kind]
		switch e.Action {
		case ActionDelete:
			s[0]++
			s[1]++
		case ActionNoop, ActionUpdate:
			s[1]++
		}
		out[kind] = s
	}
	return out
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

// ParseDir reads the declarations checkout: every *.yaml/*.yml under
// <root>/views plus, when present, <root>/credential-refs. A missing views
// directory is an error, not an empty set — an empty set prunes every
// cac-declared View, and a mistyped path must never look like one.
// (credential-refs/ is optional: repos predating ADR-0009 stay valid.)
func ParseDir(root string) (Declarations, error) {
	var out Declarations

	views, err := parseKind(filepath.Join(root, "views"), false, parseViewFile)
	if err != nil {
		return out, err
	}
	out.Views = views
	sort.Slice(out.Views, func(i, j int) bool { return out.Views[i].Name < out.Views[j].Name })

	refs, err := parseKind(filepath.Join(root, "credential-refs"), true, parseCredentialRefFile)
	if err != nil {
		return out, err
	}
	out.CredentialRefs = refs
	sort.Slice(out.CredentialRefs, func(i, j int) bool { return out.CredentialRefs[i].Name < out.CredentialRefs[j].Name })
	return out, nil
}

// parseKind reads one declaration directory; optional dirs may be absent.
func parseKind[T any](dir string, optional bool, parse func(path string, raw []byte) (string, T, error)) ([]T, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) && optional {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("desiredstate: read declarations: %w", err)
	}
	seen := map[string]string{} // declared name → file
	var out []T
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
		name, decl, err := parse(path, raw)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[name]; dup {
			return nil, fmt.Errorf("desiredstate: %q declared in both %s and %s", name, prev, path)
		}
		seen[name] = path
		out = append(out, decl)
	}
	return out, nil
}

func parseViewFile(path string, raw []byte) (string, Declaration, error) {
	var f declFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // typos in declarations must fail, not vanish
	if err := dec.Decode(&f); err != nil {
		return "", Declaration{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	if f.Name == "" {
		return "", Declaration{}, fmt.Errorf("desiredstate: %s: name is required", path)
	}
	sel, err := f.Selector.toSelector()
	if err != nil {
		return "", Declaration{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return f.Name, Declaration{Name: f.Name, Selector: sel}, nil
}

// credRefFile is the credential-refs/*.yaml shape (pointer + injection
// policy only — nothing in the declaration can hold material, §2.5).
type credRefFile struct {
	Name      string            `yaml:"name"`
	OwnerTeam string            `yaml:"ownerTeam"`
	Backend   string            `yaml:"backend"`
	Locator   map[string]any    `yaml:"locator"`
	Injection []credRefInjxYAML `yaml:"injection"`
}
type credRefInjxYAML struct {
	Key  string `yaml:"key"`
	As   string `yaml:"as"`
	Name string `yaml:"name"`
}

func parseCredentialRefFile(path string, raw []byte) (string, types.CredentialRef, error) {
	var f credRefFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.CredentialRef{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	ref, err := f.toCredentialRef()
	if err != nil {
		return "", types.CredentialRef{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return ref.Name, ref, nil
}

func (f credRefFile) toCredentialRef() (types.CredentialRef, error) {
	var ref types.CredentialRef
	if f.Name == "" || f.OwnerTeam == "" {
		return ref, fmt.Errorf("credential ref requires name and ownerTeam")
	}
	switch f.Backend {
	case types.BackendK8sSecret, types.BackendVault, types.BackendWorkloadIdentity:
	default:
		return ref, fmt.Errorf("credential ref %s: unknown backend %q", f.Name, f.Backend)
	}
	if len(f.Locator) == 0 {
		return ref, fmt.Errorf("credential ref %s: locator is required", f.Name)
	}
	locator, err := json.Marshal(f.Locator)
	if err != nil {
		return ref, err
	}
	if len(f.Injection) == 0 {
		return ref, fmt.Errorf("credential ref %s: injection policy is required", f.Name)
	}
	inj := make([]types.CredentialInjection, len(f.Injection))
	for i, x := range f.Injection {
		if x.Key == "" || x.Name == "" {
			return ref, fmt.Errorf("credential ref %s: injection %d requires key and name", f.Name, i)
		}
		if x.As != types.InjectEnv && x.As != types.InjectFile {
			return ref, fmt.Errorf("credential ref %s: injection %d: as must be env or file", f.Name, i)
		}
		inj[i] = types.CredentialInjection{Key: x.Key, As: x.As, Name: x.Name}
	}
	return types.CredentialRef{
		Name: f.Name, OwnerTeam: f.OwnerTeam, Backend: f.Backend,
		Locator: locator, Injection: inj,
	}, nil
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

// ComputePlan diffs the declarations against the graph's current state
// across every declared kind.
func ComputePlan(ctx context.Context, store *graph.Store, decls Declarations) (Plan, error) {
	plan, err := computeViewPlan(ctx, store, decls.Views)
	if err != nil {
		return Plan{}, err
	}
	refPlan, err := computeCredentialRefPlan(ctx, store, decls.CredentialRefs)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, refPlan.Entries...)
	sort.Slice(plan.Entries, func(i, j int) bool {
		if plan.Entries[i].Kind != plan.Entries[j].Kind {
			return plan.Entries[i].Kind < plan.Entries[j].Kind
		}
		return plan.Entries[i].Name < plan.Entries[j].Name
	})
	return plan, nil
}

func computeViewPlan(ctx context.Context, store *graph.Store, decls []Declaration) (Plan, error) {
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
		entry := PlanEntry{Kind: KindView, Name: d.Name, NewSelector: ptr(d.Selector)}
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
			Kind: KindView, Name: v.Name, Action: ActionDelete, MemberCount: n, OldSelector: ptr(v.Selector),
		})
	}
	return plan, nil
}

// computeCredentialRefPlan diffs declared CredentialRef pointers. Equality
// is semantic JSON equality of the pointer document (never material — none
// exists to compare, §2.5). MemberCount is not meaningful for refs.
func computeCredentialRefPlan(ctx context.Context, store *graph.Store, decls []types.CredentialRef) (Plan, error) {
	cac, err := store.ListCredentialRefsDeclaredBy(ctx, graph.DeclaredByCaC)
	if err != nil {
		return Plan{}, err
	}
	api, err := store.ListCredentialRefsDeclaredBy(ctx, graph.DeclaredByAPI)
	if err != nil {
		return Plan{}, err
	}
	cacByName := map[string]types.CredentialRef{}
	for _, r := range cac {
		cacByName[r.Name] = r
	}
	apiByName := map[string]bool{}
	for _, r := range api {
		apiByName[r.Name] = true
	}

	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindCredentialRef, Name: d.Name}
		current, isCac := cacByName[d.Name]
		switch {
		case !isCac && !apiByName[d.Name]:
			entry.Action = ActionCreate
		case apiByName[d.Name]:
			entry.Action = ActionAdopt
		case credentialRefsEqual(current, d):
			entry.Action = ActionNoop
		default:
			entry.Action = ActionUpdate
		}
		plan.Entries = append(plan.Entries, entry)
	}
	for _, r := range cac {
		if !declared[r.Name] {
			plan.Entries = append(plan.Entries, PlanEntry{
				Kind: KindCredentialRef, Name: r.Name, Action: ActionDelete,
			})
		}
	}
	return plan, nil
}

// Apply executes the plan for the declarations and returns the realized plan.
// Per-object failures are recorded on their entries; the rest still applies.
func Apply(ctx context.Context, store *graph.Store, decls Declarations) (Plan, error) {
	plan, err := ComputePlan(ctx, store, decls)
	if err != nil {
		return Plan{}, err
	}
	viewByName := map[string]types.ViewSelector{}
	for _, d := range decls.Views {
		viewByName[d.Name] = d.Selector
	}
	refByName := map[string]types.CredentialRef{}
	for _, d := range decls.CredentialRefs {
		refByName[d.Name] = d
	}
	for i := range plan.Entries {
		e := &plan.Entries[i]
		if e.Action == ActionNoop {
			continue
		}
		var err error
		switch {
		case e.Kind == KindView && e.Action == ActionDelete:
			err = store.DeleteView(ctx, e.Name, graph.DeclaredByCaC)
		case e.Kind == KindView:
			_, err = store.DeclareViewAs(ctx, e.Name, viewByName[e.Name], graph.DeclaredByCaC)
		case e.Kind == KindCredentialRef && e.Action == ActionDelete:
			err = store.DeleteCredentialRef(ctx, e.Name, graph.DeclaredByCaC)
		case e.Kind == KindCredentialRef:
			_, err = store.DeclareCredentialRefAs(ctx, refByName[e.Name], graph.DeclaredByCaC)
		}
		if err != nil {
			e.Error = err.Error()
		}
	}
	return plan, nil
}

// credentialRefsEqual compares pointer documents semantically.
func credentialRefsEqual(a, b types.CredentialRef) bool {
	a.DeclaredBy, b.DeclaredBy = "", ""
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
