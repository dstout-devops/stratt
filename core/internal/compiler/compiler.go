// Package compiler is the Intent/Assignment/Blueprint compiler (charter
// §2.4, §4, §8 Phase 2; ADR-0023): declared Intents × Assignments × live
// View membership × versioned Blueprints compile into facet-observation
// Baselines (+ remediation Workflow refs). It runs inside the desired-state
// reconcile cycle — membership drifts without Git changes (Syncer relabels),
// so the compile re-runs every pass.
//
// The charter's anti-GPO axiom is load-bearing here: there is NO implicit
// precedence. Exclusive claims that collide fail the compile (both named);
// additive claims union. Ownership is registered (a namespace has one
// Blueprint owner). The membership-delta plan surfaces which Entities
// join/leave per Assignment; the max-delta gate pauses a compile whose
// target set shifts more than a fraction between reconciles until a
// deliberate Git acknowledgement.
package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/template"
	"github.com/dstout-devops/stratt/types"
)

// DefaultMaxDelta is the engine-level max-delta fraction (§4.3) when an
// Assignment declares no override.
const DefaultMaxDelta = 0.5

// blueprintOwnerKind marks facet_owner rows a Blueprint claims.
const blueprintOwnerKind = "blueprint"

// Plan is one compile pass's outcome — computed read-only, then applied.
type Plan struct {
	// Upserts are the compiled Baselines to write.
	Upserts []types.Baseline
	// Prunes are compiler-owned Baseline names to delete (route/assignment
	// gone), excluding those of skipped Assignments (kept untouched).
	Prunes []string
	// Orphans are Findings owed for withdrawn-but-retained Assignments.
	Orphans []Orphan
	// Ownership registrations to perform (namespace → Blueprint).
	Ownership []types.FacetOwner
	// Memberships to persist for successfully-compiled Assignments.
	Memberships []graph.AssignmentMembership
	// Deltas is the per-Assignment membership-delta plan surface (§4.3).
	Deltas []AssignmentDelta
	// Errors are compile errors (claim conflict, cross-ref, ownership) —
	// surfaced, never a partial apply of the involved Assignment.
	Errors []string
}

// AssignmentDelta is the compiled-membership change for one Assignment — the
// "stratt plan renders membership deltas" surface (§4.3).
type AssignmentDelta struct {
	Assignment  string   `json:"assignment"`
	MemberCount int      `json:"memberCount"`
	Joins       []string `json:"joins,omitempty"`
	Leaves      []string `json:"leaves,omitempty"`
	Unrouted    []string `json:"unrouted,omitempty"`
	// Paused is set when the max-delta gate held this Assignment's recompile.
	Paused bool `json:"paused,omitempty"`
	// Note explains a pause or skip (§1.8: the wait is visible).
	Note string `json:"note,omitempty"`
}

// Orphan is a Finding owed for compiled state left behind by a withdrawn
// Assignment (§2.4, §4.3: abandoned state is never silent).
type Orphan struct {
	Baseline string
	Target   string
	Severity string
	Detail   []byte
}

// Store is the compiler's read surface (satisfied by *graph.Store).
type Store interface {
	ListIntents(ctx context.Context) ([]types.Intent, error)
	ListAssignments(ctx context.Context) ([]types.Assignment, error)
	GetIntent(ctx context.Context, name string) (types.Intent, error)
	GetView(ctx context.Context, name string) (types.View, error)
	ResolveSelector(ctx context.Context, sel types.ViewSelector, params map[string]any, limit int) ([]types.Entity, error)
	GetBlueprint(ctx context.Context, name string, version int) (types.Blueprint, error)
	GetWorkflow(ctx context.Context, name string) (types.Workflow, error)
	GetAssignmentMembership(ctx context.Context, assignment string) (graph.AssignmentMembership, bool, error)
	GetFacetOwner(ctx context.Context, namespace string) (types.FacetOwner, bool, error)
	ListBaselines(ctx context.Context) ([]types.Baseline, error)
}

// claimRecord is one (namespace, entity) claim for cross-Assignment conflict
// detection.
type claimRecord struct {
	namespace  string
	entityID   string
	claim      string
	assignment string
}

// ownClaim is one Blueprint's claim to manage a Facet namespace — the input
// to blueprint-vs-blueprint ownership conflict detection.
type ownClaim struct {
	namespace  string
	blueprint  string
	assignment string
}

// Compile computes the plan for one reconcile pass — read-only.
func Compile(ctx context.Context, s Store, maxDelta float64) (Plan, error) {
	if maxDelta <= 0 {
		maxDelta = DefaultMaxDelta
	}
	assignments, err := s.ListAssignments(ctx)
	if err != nil {
		return Plan{}, err
	}
	declared := map[string]bool{}
	for _, a := range assignments {
		declared[a.Name] = true
	}

	var plan Plan
	skipped := map[string]bool{}                // assignment → keep its baselines, don't prune
	candidates := map[string][]types.Baseline{} // assignment → its compiled baselines
	var claims []claimRecord
	var ownClaims []ownClaim

	for _, a := range assignments {
		delta := AssignmentDelta{Assignment: a.Name}

		// ── cross-reference validation (§2.1 cac-View guardian; existence) ──
		bp, intent, verr := validateRefs(ctx, s, a)
		if verr != "" {
			skipped[a.Name] = true
			plan.Errors = append(plan.Errors, verr)
			delta.Note = verr
			plan.Deltas = append(plan.Deltas, delta)
			continue
		}
		view, _ := s.GetView(ctx, a.View)
		members, err := resolveIDs(ctx, s, view.Selector)
		if err != nil {
			return Plan{}, err
		}
		delta.MemberCount = len(members)

		// ── membership-delta + max-delta gate (§4.3) ──
		prev, hadPrev, err := s.GetAssignmentMembership(ctx, a.Name)
		if err != nil {
			return Plan{}, err
		}
		joins, leaves := diffIDs(prev.EntityIDs, members)
		delta.Joins, delta.Leaves = joins, leaves
		effMax := maxDelta
		if a.MaxDelta != nil {
			effMax = *a.MaxDelta
		}
		if hadPrev && prev.MemberCount > 0 && exceedsDelta(prev.MemberCount, len(joins)+len(leaves), effMax) {
			if a.AckDelta <= prev.AckedDelta {
				skipped[a.Name] = true
				delta.Paused = true
				delta.Note = fmt.Sprintf("max-delta gate: %d of %d changed (> %.0f%%); bump ackDelta to acknowledge",
					len(joins)+len(leaves), prev.MemberCount, effMax*100)
				plan.Deltas = append(plan.Deltas, delta)
				continue
			}
		}

		// ── routing ──
		routed := map[string]bool{}
		for i, route := range bp.Routes {
			matched, err := routeMatch(ctx, s, view.Selector, route)
			if err != nil {
				return Plan{}, err
			}
			if len(matched) == 0 {
				continue // visible via unrouted below; no empty Baseline
			}
			for _, id := range matched {
				routed[id] = true
			}
			exp, serr := substituteExpectation(route.Observe, intent.Spec)
			if serr != "" {
				skipped[a.Name] = true
				plan.Errors = append(plan.Errors, fmt.Sprintf("assignment %s: route %d: %s", a.Name, i, serr))
				delta.Note = serr
				break
			}
			b := compiledBaseline(a, bp, intent, i, view, route, exp, matched)
			candidates[a.Name] = append(candidates[a.Name], b)
			for _, id := range matched {
				claims = append(claims, claimRecord{exp.Namespace, id, route.Claim, a.Name})
			}
			// Ownership is claimed only for a namespace the Blueprint MANAGES
			// (writes, via a remediation Workflow). A pure observation reads a
			// Facet — often Syncer-projected, like os.kernel — and never
			// seizes write-ownership (§2.1; guardian on ADR-0023).
			if route.RemediationWorkflow != "" {
				ownClaims = append(ownClaims, ownClaim{exp.Namespace, bp.Name, a.Name})
			}
		}
		if skipped[a.Name] {
			plan.Deltas = append(plan.Deltas, delta)
			continue
		}
		for _, id := range members {
			if !routed[id] {
				delta.Unrouted = append(delta.Unrouted, id)
			}
		}
		newAcked := prev.AckedDelta
		if a.AckDelta > newAcked {
			newAcked = a.AckDelta
		}
		plan.Memberships = append(plan.Memberships, graph.AssignmentMembership{
			Assignment: a.Name, EntityIDs: members, MemberCount: len(members), AckedDelta: newAcked,
		})
		plan.Deltas = append(plan.Deltas, delta)
	}

	// ── claim resolution across all non-skipped Assignments (anti-GPO) ──
	for _, poisoned := range detectClaimConflicts(claims, skipped) {
		skipped[poisoned.assignment] = true
		delete(candidates, poisoned.assignment)
		plan.Errors = append(plan.Errors, poisoned.message)
	}

	// ── ownership registry (blueprint-vs-blueprint, §2.1) ──
	// A namespace already owned by a Syncer or team is observed read-only —
	// reads never claim write-ownership. An unowned namespace claimed by more
	// than one distinct Blueprint (in this pass or against a persisted
	// Blueprint owner) is a conflict: those Assignments are poisoned.
	ownerships, ownConflicts, err := resolveOwnership(ctx, s, ownClaims, skipped)
	if err != nil {
		return Plan{}, err
	}
	for _, c := range ownConflicts {
		for _, a := range c.assignments {
			skipped[a] = true
			delete(candidates, a)
		}
		plan.Errors = append(plan.Errors, c.message)
	}
	plan.Ownership = ownerships

	// Drop memberships contributed by now-poisoned Assignments.
	plan.Memberships = filterMemberships(plan.Memberships, skipped)

	// ── assemble upserts + prune/orphan ──
	desired := map[string]bool{}
	for name, bs := range candidates {
		if skipped[name] {
			continue
		}
		for _, b := range bs {
			plan.Upserts = append(plan.Upserts, b)
			desired[b.Name] = true
		}
	}

	existing, err := s.ListBaselines(ctx)
	if err != nil {
		return Plan{}, err
	}
	for _, eb := range existing {
		if eb.CompiledFrom == nil || desired[eb.Name] {
			continue
		}
		asg := eb.CompiledFrom.Assignment
		if skipped[asg] {
			continue // keep a skipped/paused Assignment's prior baselines
		}
		plan.Prunes = append(plan.Prunes, eb.Name)
		if !declared[asg] {
			// Withdrawn Assignment: onRemove=retain (v1) → orphan Finding.
			detail, _ := json.Marshal(map[string]any{
				"reason": "assignment withdrawn; compiled state retained (onRemove=retain)",
			})
			plan.Orphans = append(plan.Orphans, Orphan{
				Baseline: eb.Name, Target: "assignment:" + asg,
				Severity: eb.Severity, Detail: detail,
			})
		}
	}
	sort.Strings(plan.Prunes)
	sort.Slice(plan.Upserts, func(i, j int) bool { return plan.Upserts[i].Name < plan.Upserts[j].Name })
	sort.Slice(plan.Deltas, func(i, j int) bool { return plan.Deltas[i].Assignment < plan.Deltas[j].Assignment })
	return plan, nil
}

// validateRefs checks the cross-references an Assignment depends on: the View
// must be cac-declared (§2.1 guardian), and the Intent, Blueprint@version,
// and each route's remediation Workflow must exist. Returns the resolved
// Blueprint + Intent, or a non-empty error string.
func validateRefs(ctx context.Context, s Store, a types.Assignment) (types.Blueprint, types.Intent, string) {
	view, err := s.GetView(ctx, a.View)
	if err != nil {
		return types.Blueprint{}, types.Intent{}, fmt.Sprintf("assignment %s: view %q not found", a.Name, a.View)
	}
	if view.DeclaredBy != graph.DeclaredByCaC {
		return types.Blueprint{}, types.Intent{}, fmt.Sprintf(
			"assignment %s: view %q is not cac-declared — an Assignment may not target an api View (desired state must stay in Git, §2.1)", a.Name, a.View)
	}
	if selectorParametrized(view.Selector) {
		return types.Blueprint{}, types.Intent{}, fmt.Sprintf(
			"assignment %s: view %q is parametrized ({{.param.x}}) — parametrized Views bind only at launch, not as a compile target (ADR-0024: the max-delta gate is undefined against param variance)", a.Name, a.View)
	}
	intent, err := s.GetIntent(ctx, a.Intent)
	if err != nil {
		return types.Blueprint{}, types.Intent{}, fmt.Sprintf("assignment %s: intent %q not found", a.Name, a.Intent)
	}
	bp, err := s.GetBlueprint(ctx, a.Blueprint, a.BlueprintVersion)
	if err != nil {
		return types.Blueprint{}, types.Intent{}, fmt.Sprintf("assignment %s: blueprint %s@%d not found", a.Name, a.Blueprint, a.BlueprintVersion)
	}
	if bp.For != intent.Kind {
		return types.Blueprint{}, types.Intent{}, fmt.Sprintf("assignment %s: blueprint %s@%d is for %q, intent %q is %q",
			a.Name, a.Blueprint, a.BlueprintVersion, bp.For, a.Intent, intent.Kind)
	}
	for i, r := range bp.Routes {
		if r.RemediationWorkflow != "" {
			if _, err := s.GetWorkflow(ctx, r.RemediationWorkflow); err != nil {
				return types.Blueprint{}, types.Intent{}, fmt.Sprintf(
					"assignment %s: blueprint route %d remediation workflow %q not found", a.Name, i, r.RemediationWorkflow)
			}
		}
	}
	return bp, intent, ""
}

// selectorParametrized reports whether a View selector carries {{.param.x}}
// placeholders (ADR-0024) — such a View resolves only with launch-supplied
// params and cannot be an Assignment/Baseline compile target.
func selectorParametrized(sel types.ViewSelector) bool {
	for _, v := range sel.Labels {
		if strings.Contains(v, "{{") {
			return true
		}
	}
	for _, f := range sel.Facets {
		if strings.Contains(string(f.Equals), "{{") {
			return true
		}
	}
	return false
}

// compiledBaseline builds one facet-observation Baseline for an (Assignment,
// route) pair. The name is deterministic and origin-stamped so the compiler
// owns exactly its rows.
func compiledBaseline(a types.Assignment, bp types.Blueprint, intent types.Intent, routeIdx int, view types.View, route types.BlueprintRoute, exp types.FacetExpectation, _ []string) types.Baseline {
	sel := types.ViewSelector{
		Kinds:  view.Selector.Kinds,
		Labels: view.Selector.Labels,
		Facets: append(append([]types.FacetPredicate{}, view.Selector.Facets...), route.Match...),
	}
	return types.Baseline{
		Name:                CompiledName(a.Name, bp.Name, bp.Version, routeIdx),
		Mode:                types.FacetObservation,
		ViewName:            a.View,
		Selector:            &sel,
		Expected:            []types.FacetExpectation{exp},
		Claim:               route.Claim,
		Cron:                "@every 1m",
		Severity:            severityOr(bp.Severity),
		DampingObservations: bp.DampingObservations,
		RemediationWorkflow: route.RemediationWorkflow,
		Framework:           "intent",
		CompiledFrom: &types.CompiledOrigin{
			Assignment: a.Name, Intent: intent.Name, Blueprint: bp.Name,
			BlueprintVersion: bp.Version, Route: routeIdx,
		},
	}
}

func severityOr(s string) string {
	if s == "" {
		return types.SeverityWarning
	}
	return s
}

// CompiledName is the deterministic name of a compiled Baseline. Dash-joined
// (schedule-id safe) and origin-legible.
func CompiledName(assignment, blueprint string, version, route int) string {
	return fmt.Sprintf("compiled-%s-%s-v%d-r%d", assignment, blueprint, version, route)
}

// resolveIDs resolves a selector to a sorted slice of Entity ids.
func resolveIDs(ctx context.Context, s Store, sel types.ViewSelector) ([]string, error) {
	ents, err := s.ResolveSelector(ctx, sel, nil, 0)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(ents))
	for _, e := range ents {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

// routeMatch resolves the route-matched subset (View selector ∩ route match).
func routeMatch(ctx context.Context, s Store, viewSel types.ViewSelector, route types.BlueprintRoute) ([]string, error) {
	sel := types.ViewSelector{
		Kinds:  viewSel.Kinds,
		Labels: viewSel.Labels,
		Facets: append(append([]types.FacetPredicate{}, viewSel.Facets...), route.Match...),
	}
	return resolveIDs(ctx, s, sel)
}

// diffIDs returns the ids in cur not in prev (joins) and in prev not in cur
// (leaves). Both inputs are treated as sets.
func diffIDs(prev, cur []string) (joins, leaves []string) {
	p := map[string]bool{}
	for _, id := range prev {
		p[id] = true
	}
	c := map[string]bool{}
	for _, id := range cur {
		c[id] = true
		if !p[id] {
			joins = append(joins, id)
		}
	}
	for _, id := range prev {
		if !c[id] {
			leaves = append(leaves, id)
		}
	}
	sort.Strings(joins)
	sort.Strings(leaves)
	return joins, leaves
}

// exceedsDelta reports whether the change count exceeds the fraction of the
// previous member count.
func exceedsDelta(prevCount, changed int, maxDelta float64) bool {
	if prevCount == 0 {
		return false
	}
	return float64(changed)/float64(prevCount) > maxDelta
}

// substituteExpectation applies Intent-spec substitution ({{.spec.X}}) to an
// observe expectation's Path and Equals/Contains values, via the shared
// explicit-lookup engine (ADR-0024).
func substituteExpectation(exp types.FacetExpectation, spec map[string]any) (types.FacetExpectation, string) {
	if exp.Namespace == "" {
		return exp, "observe expectation requires a namespace"
	}
	ns := template.Namespaces{"spec": spec}
	path, err := template.Substitute(exp.Path, ns)
	if err != nil {
		return exp, err.Error()
	}
	exp.Path, _ = path.(string)
	if exp.Equals, err = substituteRaw(exp.Equals, ns); err != nil {
		return exp, err.Error()
	}
	if exp.Contains, err = substituteRaw(exp.Contains, ns); err != nil {
		return exp, err.Error()
	}
	if len(exp.Equals) == 0 && len(exp.Contains) == 0 {
		return exp, "observe expectation requires equals or contains"
	}
	return exp, ""
}

// substituteRaw resolves templates inside a JSON value: unmarshal → substitute
// (type-preserving) → re-marshal. Empty/invalid JSON passes through.
func substituteRaw(raw json.RawMessage, ns template.Namespaces) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw, nil
	}
	if !template.Has(v) {
		return raw, nil
	}
	out, err := template.Substitute(v, ns)
	if err != nil {
		return nil, err
	}
	nb, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return nb, nil
}

type poison struct {
	assignment string
	message    string
}

// detectClaimConflicts finds exclusive claims over the same (namespace,
// entity) held by more than one Assignment — a compile error naming all
// claimants (the anti-GPO axiom, §2.4). Skipped Assignments' claims are
// ignored. Every Assignment involved in a conflict is poisoned.
func detectClaimConflicts(claims []claimRecord, skipped map[string]bool) []poison {
	type key struct{ ns, entity string }
	exclusive := map[key]map[string]bool{}
	for _, c := range claims {
		if skipped[c.assignment] || c.claim != types.ClaimExclusive {
			continue
		}
		k := key{c.namespace, c.entityID}
		if exclusive[k] == nil {
			exclusive[k] = map[string]bool{}
		}
		exclusive[k][c.assignment] = true
	}
	poisoned := map[string]bool{}
	messages := map[string]string{}
	for k, asgs := range exclusive {
		if len(asgs) < 2 {
			continue
		}
		names := make([]string, 0, len(asgs))
		for a := range asgs {
			names = append(names, a)
		}
		sort.Strings(names)
		msg := fmt.Sprintf("exclusive claim conflict on facet %q for entity %s: assignments %s (§2.4: no implicit precedence — resolve by scoping, not priority)",
			k.ns, k.entity, strings.Join(names, ", "))
		for _, a := range names {
			poisoned[a] = true
			if _, ok := messages[a]; !ok {
				messages[a] = msg
			}
		}
	}
	out := make([]poison, 0, len(poisoned))
	for a := range poisoned {
		out = append(out, poison{assignment: a, message: messages[a]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].assignment < out[j].assignment })
	return out
}

type ownConflict struct {
	assignments []string
	message     string
}

// resolveOwnership decides which Blueprint ownership registrations to perform
// and which namespaces are contested (§2.1). A namespace already owned by a
// Syncer or team is read-observed — no claim. An unowned namespace claimed by
// more than one distinct Blueprint (this pass, or vs. a persisted Blueprint
// owner) is a conflict poisoning every claimant Assignment.
func resolveOwnership(ctx context.Context, s Store, claims []ownClaim, skipped map[string]bool) ([]types.FacetOwner, []ownConflict, error) {
	// namespace → blueprint → assignments claiming it.
	byNS := map[string]map[string][]string{}
	for _, c := range claims {
		if skipped[c.assignment] {
			continue
		}
		if byNS[c.namespace] == nil {
			byNS[c.namespace] = map[string][]string{}
		}
		byNS[c.namespace][c.blueprint] = append(byNS[c.namespace][c.blueprint], c.assignment)
	}

	var owners []types.FacetOwner
	var conflicts []ownConflict
	namespaces := make([]string, 0, len(byNS))
	for ns := range byNS {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)

	for _, ns := range namespaces {
		blueprints := byNS[ns]
		owner, owned, err := s.GetFacetOwner(ctx, ns)
		if err != nil {
			return nil, nil, err
		}
		if owned && owner.OwnerKind != blueprintOwnerKind {
			continue // Syncer/team-owned: read-only observation, no claim
		}
		// Distinct Blueprint claimants (include a persisted Blueprint owner).
		claimants := map[string]bool{}
		for bp := range blueprints {
			claimants[bp] = true
		}
		if owned {
			claimants[owner.OwnerRef] = true
		}
		if len(claimants) > 1 {
			names := make([]string, 0)
			for _, as := range blueprints {
				names = append(names, as...)
			}
			sort.Strings(names)
			bpNames := make([]string, 0, len(claimants))
			for bp := range claimants {
				bpNames = append(bpNames, bp)
			}
			sort.Strings(bpNames)
			conflicts = append(conflicts, ownConflict{
				assignments: names,
				message: fmt.Sprintf("facet namespace %q is claimed by multiple Blueprints (%s) — one namespace has one write owner (§2.1); scope the routes, never share ownership",
					ns, strings.Join(bpNames, ", ")),
			})
			continue
		}
		// Exactly one Blueprint — register it (idempotent if already owner).
		for bp := range blueprints {
			owners = append(owners, types.FacetOwner{Namespace: ns, OwnerKind: blueprintOwnerKind, OwnerRef: bp})
		}
	}
	return owners, conflicts, nil
}

func filterMemberships(ms []graph.AssignmentMembership, skipped map[string]bool) []graph.AssignmentMembership {
	out := ms[:0]
	for _, m := range ms {
		if !skipped[m.Assignment] {
			out = append(out, m)
		}
	}
	return out
}
