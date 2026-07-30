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

	"github.com/dstout-devops/stratt/core/internal/capability"
	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/overlay"
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
	// ExpectationChanges are the compiled expectations whose VALUE changed since the last
	// compile — the §4.3 surface for a change the membership delta above cannot see
	// (ADR-0119 D5). A pinned-version bump rewrites expected values while joins and leaves
	// stay empty, so before this existed a promotion was invisible to every runtime gate.
	ExpectationChanges []ExpectationChange `json:"expectationChanges,omitempty"`
	// Paused is set when the max-delta gate held this Assignment's recompile.
	Paused bool `json:"paused,omitempty"`
	// Note explains a pause or skip (§1.8: the wait is visible).
	Note string `json:"note,omitempty"`
}

// ExpectationChange is one compiled expectation whose value differs from the previous
// compile (ADR-0119 D5). Rendered so "what does promoting this actually change" is
// answerable before the change lands, rather than inferred from a Git diff of the Intent.
//
// From/To are the rendered JSON of the expectation's value, not structured — the point is a
// human-readable diff, and an expectation is one of Equals/Contains/NotBefore, so a single
// string column keeps the surface honest about what it is.
type ExpectationChange struct {
	Baseline  string `json:"baseline"`
	Namespace string `json:"namespace"`
	Path      string `json:"path,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
}

// Orphan is a Finding owed for compiled state left behind by a withdrawn
// Assignment (§2.4, §4.3: abandoned state is never silent).
type Orphan struct {
	Baseline string
	Target   string
	Severity string
	Detail   []byte
	// LaunchWorkflow + LaunchParams are the withdrawal launch spec carried ONTO the orphan
	// Finding, because the Baseline they came from is pruned in the same Apply (ADR-0118 D3,
	// ADR-0120 D1). Without them the Finding names abandoned state and offers no way to
	// retire it.
	LaunchWorkflow string
	LaunchParams   map[string]any
}

// Store is the compiler's read surface (satisfied by *graph.Store).
type Store interface {
	ListIntents(ctx context.Context) ([]types.Intent, error)
	ListAssignments(ctx context.Context) ([]types.Assignment, error)
	GetIntent(ctx context.Context, name string, version int) (types.Intent, error)
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

// RemediationResolver binds a route's `remediationCapability` + bare Intent kind to a provider and
// its convergence Workflow (ADR-0135 D3). Supplied by the caller rather than reached through Store,
// because WHICH providers count is an estate concern — the verified index and the environment
// filter — and the compiler has no business re-deriving either.
//
// A nil resolver makes every capability-routed route fail closed with a diagnosis rather than
// silently compiling a Baseline with no remediation (§1.8).
type RemediationResolver func(ctx context.Context, capClass, intentKind string) (capability.Result, error)

// Compile computes the plan for one reconcile pass — read-only.
func Compile(ctx context.Context, s Store, maxDelta float64, resolveRemediation RemediationResolver) (Plan, error) {
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

	// Read the previously-compiled Baselines ONCE, before the loop: the prune below needs them,
	// and so does the per-Assignment expectation diff (ADR-0119 D5), which has to compare what
	// this pass would write against what is already live.
	existing, err := s.ListBaselines(ctx)
	if err != nil {
		return Plan{}, err
	}
	priorByName := make(map[string]types.Baseline, len(existing))
	for _, eb := range existing {
		priorByName[eb.Name] = eb
	}

	var plan Plan
	skipped := map[string]bool{}                // assignment → keep its baselines, don't prune
	candidates := map[string][]types.Baseline{} // assignment → its compiled baselines
	var claims []claimRecord
	var ownClaims []ownClaim

	for _, a := range assignments {
		delta := AssignmentDelta{Assignment: a.Name}

		// ── cross-reference validation (§2.1 cac-View guardian; existence) ──
		bp, intent, verr := validateRefs(ctx, s, a, resolveRemediation)
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

		// G6 (ADR-0083 §5): the EFFECTIVE Intent spec = the Blueprint's YIELDING defaults
		// filling whatever the Intent's DECLARING spec leaves unset — explicit overlay
		// layering with no precedence field and no rung order among declarations
		// (§2.4/§4.1 anti-GPO, ADR-0118 D1). Routes substitute {{.spec.X}} from THIS
		// resolved spec, so a field the Intent omits takes the Blueprint default. A
		// cross-type clash, or two DECLARING layers asserting one path, fails this
		// Assignment loudly (§1.8) — never coerced, never silently resolved.
		resolvedSpec, specLayers, merr := overlay.Merge([]overlay.Layer{
			{Name: "blueprint:" + bp.Name + "/defaults", Values: bp.Defaults, Yielding: true},
			{Name: "intent:" + intent.Name, Values: intent.Spec},
			{Name: "assignment:" + a.Name, Values: a.Values},
		})
		if merr != nil {
			skipped[a.Name] = true
			plan.Errors = append(plan.Errors, fmt.Sprintf("assignment %s: defaults/spec merge: %v", a.Name, merr))
			delta.Note = merr.Error()
		}
		// The MERGED spec is where completeness is judged (ADR-0118 D1). Each layer is
		// validated as PARTIAL at declaration, because a layer legitimately holds a
		// fragment — an Intent that leaves a field to its Assignment must not fail its own
		// declaration. So the kind's schema is enforced HERE, once, against the whole
		// resolved document; without this the omit-to-override rule would have quietly
		// dropped required-field enforcement altogether. Same precedent as ADR-0024 D4:
		// validate resolved data, not placeholders.
		if !skipped[a.Name] {
			if verr := validateResolvedSpec(intent.Kind, resolvedSpec); verr != nil {
				skipped[a.Name] = true
				plan.Errors = append(plan.Errors, fmt.Sprintf("assignment %s: %v", a.Name, verr))
				delta.Note = verr.Error()
			}
		}

		// The WITHDRAWAL params, resolved once per Assignment rather than per route: the
		// Blueprint's removeWorkflow retires the whole compiled set, not one route's
		// expectation. Stamped onto every Baseline below so the orphan branch can read them
		// after the Assignment is gone from Git (ADR-0118 D3).
		var remvParams map[string]any
		if !skipped[a.Name] {
			var rverr string
			remvParams, rverr = resolveRemoveParams(ctx, s, bp, resolvedSpec)
			if rverr != "" {
				skipped[a.Name] = true
				plan.Errors = append(plan.Errors, fmt.Sprintf("assignment %s: %s", a.Name, rverr))
				delta.Note = rverr
			}
		}

		// ── routing ──
		routed := map[string]bool{}
		for i, route := range bp.Routes {
			if skipped[a.Name] {
				break // a merge-failed (or already-skipped) Assignment routes nothing
			}
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
			exp, serr := substituteExpectation(route.Observe, resolvedSpec)
			if serr != "" {
				skipped[a.Name] = true
				plan.Errors = append(plan.Errors, fmt.Sprintf("assignment %s: route %d: %s", a.Name, i, serr))
				delta.Note = serr
				break
			}
			// The route's params for its remediation Workflow, resolved from the SAME spec the
			// expectation is (ADR-0118 D3) — so what we expect and what we would do to fix it
			// are stated once, in one place, instead of the Workflow re-declaring them.
			remParams, rerr := resolveRemediationParams(ctx, s, route, resolvedSpec)
			if rerr != "" {
				skipped[a.Name] = true
				plan.Errors = append(plan.Errors, fmt.Sprintf("assignment %s: route %d: %s", a.Name, i, rerr))
				delta.Note = rerr
				break
			}
			b := compiledBaseline(a, bp, intent, i, view, route, exp, matched, specLayers, remParams, remvParams)
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
		// ── expectation-change gate (§4.3, ADR-0119 D5) ──
		// The membership gate above cannot see a pinned-version bump: promoting a configuration
		// rewrites expected VALUES while joins and leaves stay empty, so `exceedsDelta` is
		// structurally incapable of firing on it. Without this, a promotion silently replaced every
		// expectation across the Assignment's whole target set, gated by nothing but code review.
		//
		// Deliberately the SAME MaxDelta fraction and the SAME AckDelta counter as membership,
		// rather than a second pair. §4.3's acknowledgement means "I have reviewed this
		// Assignment's pending change"; two independent acks would let an operator acknowledge a
		// membership shift while ignoring a total expectation rewrite, which is the worse failure.
		// One ack, both axes.
		changes, total := expectationChanges(candidates[a.Name], priorByName)
		delta.ExpectationChanges = changes
		if !skipped[a.Name] && total > 0 && len(changes) > 0 &&
			exceedsDelta(total, len(changes), effMax) && a.AckDelta <= prev.AckedDelta {
			skipped[a.Name] = true
			delta.Paused = true
			delta.Note = fmt.Sprintf(
				"expectation-change gate: %d of %d compiled expectations change (> %.0f%%); "+
					"bump ackDelta to acknowledge. The live expectations stay in force until you do",
				len(changes), total, effMax*100)
			plan.Deltas = append(plan.Deltas, delta)
			continue
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

	// ── ownership registry (§2.1) ──
	// A namespace already owned by a Syncer or team is observed read-only —
	// reads never claim write-ownership. Otherwise every claimant Blueprint is
	// registered as an owner: registration says who MAY write, and since
	// ADR-0060 that is per (namespace, owner_ref). Contention is decided
	// per-Entity by detectClaimConflicts above, not here.
	ownerships, err := resolveOwnership(ctx, s, ownClaims, skipped)
	if err != nil {
		return Plan{}, err
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
			// Withdrawn Assignment. Default onRemove=retain → an orphan Finding
			// (abandoned state is never silent, §2.4/§4.3).
			detail := map[string]any{
				"reason": "assignment withdrawn; compiled state retained (onRemove=retain)",
			}
			// onRemove: remove | revert (§2.4, ADR-0030/0036) — consult the
			// still-declared Intent + Blueprint to surface a decommission/restore
			// remediation on the orphan. remove decommissions (revoke a cert, an
			// access grant); revert restores prior state (remove a distributed
			// file, remove a granted access). Both surface the Blueprint's
			// removeWorkflow — a ref only: the operator launches it, never auto-run
			// (§5 Flow 2, §1.8). If the Intent is also gone we cannot know its
			// removal semantics → retain.
			// Read the Intent AT THE VERSION THIS BASELINE WAS COMPILED FROM (ADR-0119 D4): the
			// withdrawal semantics that apply are the ones the compiled state was produced under,
			// not whatever the newest version happens to say.
			orphan := Orphan{
				Baseline: eb.Name, Target: "assignment:" + asg, Severity: eb.Severity,
			}
			if in, err := s.GetIntent(ctx, eb.CompiledFrom.Intent, eb.CompiledFrom.IntentVersion); err == nil &&
				(in.OnRemove == types.OnRemoveRemove || in.OnRemove == types.OnRemoveRevert) {
				if bp, err := s.GetBlueprint(ctx, eb.CompiledFrom.Blueprint, eb.CompiledFrom.BlueprintVersion); err == nil && bp.RemoveWorkflow != "" {
					detail["reason"] = fmt.Sprintf("assignment withdrawn with onRemove=%s; launch the remove workflow to %s (never auto-run, §5 Flow 2)",
						in.OnRemove, removeVerb(in.OnRemove))
					detail["onRemove"] = in.OnRemove
					detail["removeWorkflow"] = bp.RemoveWorkflow
					// The params come off the BASELINE, not off the Blueprint we just read:
					// they were resolved from a spec that included the now-withdrawn
					// Assignment's values, so this row is their only surviving record. The
					// ref above is read live because the pinned Blueprint still declares it
					// (§1.2 — each fact from its one authority).
					if len(eb.RemoveParams) > 0 {
						detail["removeParams"] = eb.RemoveParams
					}
					// The same two values, TYPED, so the withdrawal is LAUNCHABLE and not merely
					// readable. detail above is the human-facing blob — it lands in
					// graph.finding.diff, documented as redacted and size-capped — so a launch
					// that parsed its way back out of it would break the day anything capped it,
					// with no failing test to notice.
					orphan.LaunchWorkflow = bp.RemoveWorkflow
					orphan.LaunchParams = eb.RemoveParams
				}
			}
			orphan.Detail, _ = json.Marshal(detail)
			plan.Orphans = append(plan.Orphans, orphan)
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
func validateRefs(ctx context.Context, s Store, a types.Assignment, resolveRemediation RemediationResolver) (types.Blueprint, types.Intent, string) {
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
	// Pinned, like the Blueprint below it (ADR-0119 D2): an Assignment names WHICH version of the
	// Intent it means, so editing another version cannot change what this environment is running.
	// The version is in the message because "intent tls-app not found" while tls-app sits in Git
	// sends an operator to the wrong place (F4).
	intent, err := s.GetIntent(ctx, a.Intent, a.IntentVersion)
	if err != nil {
		return types.Blueprint{}, types.Intent{}, fmt.Sprintf(
			"assignment %s: intent %s@%d not found", a.Name, a.Intent, a.IntentVersion)
	}
	bp, err := s.GetBlueprint(ctx, a.Blueprint, a.BlueprintVersion)
	if err != nil {
		return types.Blueprint{}, types.Intent{}, fmt.Sprintf("assignment %s: blueprint %s@%d not found", a.Name, a.Blueprint, a.BlueprintVersion)
	}
	if bp.For != intent.Kind {
		return types.Blueprint{}, types.Intent{}, fmt.Sprintf("assignment %s: blueprint %s@%d is for %q, intent %q is %q",
			a.Name, a.Blueprint, a.BlueprintVersion, bp.For, a.Intent, intent.Kind)
	}
	// Resolve capability-routed remediation to a CONCRETE Workflow before anything downstream
	// reads the route (ADR-0135 D3). Everything after this point — the ownership claim, the params
	// cross-check, the compiled Baseline — sees a plain RemediationWorkflow and needs no change,
	// which is the point: the indirection ends here, at compile, so descent shows one answer.
	for i := range bp.Routes {
		r := &bp.Routes[i]
		if r.RemediationCapability == "" {
			continue
		}
		if resolveRemediation == nil {
			return types.Blueprint{}, types.Intent{}, fmt.Sprintf(
				"assignment %s: blueprint route %d names remediationCapability %q but this compile has no capability resolver — refusing to compile a Baseline whose remediation would be silently absent (ADR-0135 D3)",
				a.Name, i, r.RemediationCapability)
		}
		res, err := resolveRemediation(ctx, r.RemediationCapability, shortIntentKind(intent.Kind))
		if err != nil {
			return types.Blueprint{}, types.Intent{}, fmt.Sprintf(
				"assignment %s: blueprint route %d remediationCapability %q: %v", a.Name, i, r.RemediationCapability, err)
		}
		if res.Status != capability.StatusResolved {
			// PENDING and AMBIGUOUS both carry the resolver's own reason, which names the fix
			// (declare/verify a provider, or add a capability-binding) — §1.8, and identical to
			// how provisioning reports it.
			return types.Blueprint{}, types.Intent{}, fmt.Sprintf(
				"assignment %s: blueprint route %d remediationCapability %q: %s", a.Name, i, r.RemediationCapability, res.Reason)
		}
		r.RemediationWorkflow = res.Workflow
	}
	for i, r := range bp.Routes {
		if r.RemediationWorkflow != "" {
			if _, err := s.GetWorkflow(ctx, r.RemediationWorkflow); err != nil {
				return types.Blueprint{}, types.Intent{}, fmt.Sprintf(
					"assignment %s: blueprint route %d remediation workflow %q not found", a.Name, i, r.RemediationWorkflow)
			}
		}
	}
	if bp.RemoveWorkflow != "" {
		if _, err := s.GetWorkflow(ctx, bp.RemoveWorkflow); err != nil {
			return types.Blueprint{}, types.Intent{}, fmt.Sprintf(
				"assignment %s: blueprint remove workflow %q not found", a.Name, bp.RemoveWorkflow)
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
func compiledBaseline(a types.Assignment, bp types.Blueprint, intent types.Intent, routeIdx int, view types.View, route types.BlueprintRoute, exp types.FacetExpectation, _ []string, specLayers map[string][]string, remParams, remvParams map[string]any) types.Baseline {
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
		Environments:        a.Environments, // inherit env scope (ADR-0057): dev-compiled ⇒ invisible to prod prune
		Cron:                "@every 1m",
		Severity:            severityOr(bp.Severity),
		DampingObservations: bp.DampingObservations,
		RemediationWorkflow: route.RemediationWorkflow,
		RemediationParams:   remParams,
		RemoveParams:        remvParams,
		Framework:           "intent",
		CompiledFrom: &types.CompiledOrigin{
			Assignment: a.Name, Intent: intent.Name, IntentVersion: intent.Version,
			Blueprint: bp.Name, BlueprintVersion: bp.Version, Route: routeIdx,
			SpecLayers: specLayers,
		},
	}
}

// resolveRemediationParams substitutes a route's remediationParams from the resolved spec and
// checks them against the named Workflow's declared input schema (ADR-0118 D3).
//
// The cross-check is the part that earns its keep. A route wired to a Workflow it does not
// fit — an unknown key, a wrongly-typed value, a required input nobody supplies — fails the
// COMPILE, so the failure lands on the person editing the declaration. Without it the same
// mistake would surface as a failed remediation launch, at the worst possible moment: an
// operator responding to a Finding, told only that their launch was rejected.
//
// Returns a non-empty string on failure (the compiler's convention for a per-Assignment skip
// reason, matching substituteExpectation).
func resolveRemediationParams(ctx context.Context, s Store, route types.BlueprintRoute, spec map[string]any) (map[string]any, string) {
	if route.RemediationWorkflow == "" {
		if len(route.RemediationParams) > 0 {
			// A half-declaration: params with nothing to pass them to. Rejected rather than
			// ignored, the same rule as facetNamespaces without identitySchemes (ADR-0117).
			return nil, "remediationParams declared with no remediationWorkflow to pass them to"
		}
		return nil, ""
	}
	if len(route.RemediationParams) == 0 {
		return nil, ""
	}
	resolved, err := template.SubstituteParams(route.RemediationParams, template.Namespaces{"spec": spec})
	if err != nil {
		return nil, fmt.Sprintf("remediationParams: %v", err)
	}
	// validateRefs already proved the Workflow exists, so a read failure here is not the
	// missing-ref case and should surface as itself.
	wf, err := s.GetWorkflow(ctx, route.RemediationWorkflow)
	if err != nil {
		return nil, fmt.Sprintf("remediationParams: read workflow %s: %v", route.RemediationWorkflow, err)
	}
	if _, err := contract.ResolveLaunchInputs(wf.Name, wf.Inputs, resolved); err != nil {
		return nil, fmt.Sprintf("remediationParams do not satisfy workflow %s: %v", wf.Name, err)
	}
	return resolved, ""
}

// resolveRemoveParams substitutes a Blueprint's removeParams from the resolved spec and checks
// them against removeWorkflow's declared input schema (ADR-0118 D3) — the withdrawal half of
// the same rule resolveRemediationParams applies to a route.
//
// Blueprint-level, because removeWorkflow is: one withdrawal retires the Assignment's whole
// compiled set. The result is stamped on every Baseline the Assignment compiles, which is what
// makes withdrawal work at all — see types.Baseline.RemoveParams: the resolved spec includes
// the Assignment's own values, and withdrawal is precisely the moment the Assignment no longer
// exists to be read.
//
// Returns a non-empty string on failure, the compiler's per-Assignment skip convention.
func resolveRemoveParams(ctx context.Context, s Store, bp types.Blueprint, spec map[string]any) (map[string]any, string) {
	if bp.RemoveWorkflow == "" {
		if len(bp.RemoveParams) > 0 {
			// Params with nothing to pass them to — the same half-declaration refusal
			// remediationParams gets, rather than silently ignoring an author's intent.
			return nil, "removeParams declared with no removeWorkflow to pass them to"
		}
		return nil, ""
	}
	if len(bp.RemoveParams) == 0 {
		return nil, ""
	}
	resolved, err := template.SubstituteParams(bp.RemoveParams, template.Namespaces{"spec": spec})
	if err != nil {
		return nil, fmt.Sprintf("removeParams: %v", err)
	}
	wf, err := s.GetWorkflow(ctx, bp.RemoveWorkflow)
	if err != nil {
		return nil, fmt.Sprintf("removeParams: read workflow %s: %v", bp.RemoveWorkflow, err)
	}
	if _, err := contract.ResolveLaunchInputs(wf.Name, wf.Inputs, resolved); err != nil {
		return nil, fmt.Sprintf("removeParams do not satisfy workflow %s: %v", wf.Name, err)
	}
	return resolved, ""
}

// expectationChanges diffs the expectations this pass would write against the ones already
// compiled, returning the changed set and the total examined (ADR-0119 D5).
//
// A Baseline with no prior row is a CREATE, not a change: its expectations are new, and counting
// them as changes would make the first compile of any Assignment look like a total rewrite. Same
// for an expectation index that did not exist before.
//
// Compares the rendered value rather than the struct, because an expectation carries exactly one
// of Equals/Contains/NotBefore and the question is only "is the asserted value different".
func expectationChanges(compiled []types.Baseline, prior map[string]types.Baseline) ([]ExpectationChange, int) {
	var out []ExpectationChange
	total := 0
	for _, b := range compiled {
		pb, had := prior[b.Name]
		for i, exp := range b.Expected {
			total++
			if !had || i >= len(pb.Expected) {
				continue // new Baseline or new expectation: a create, not a change
			}
			from, to := expectationValue(pb.Expected[i]), expectationValue(exp)
			if from == to {
				continue
			}
			out = append(out, ExpectationChange{
				Baseline: b.Name, Namespace: exp.Namespace, Path: exp.Path, From: from, To: to,
			})
		}
	}
	return out, total
}

// expectationValue renders the one assertion an expectation carries, for display and comparison.
func expectationValue(e types.FacetExpectation) string {
	switch {
	case len(e.Equals) > 0:
		return "equals " + string(e.Equals)
	case len(e.Contains) > 0:
		return "contains " + string(e.Contains)
	case e.NotBefore != "":
		return "notBefore " + e.NotBefore
	default:
		return ""
	}
}

// validateResolvedSpec enforces the Intent kind's schema against the MERGED spec — the
// completeness check that moved here from declaration time (ADR-0118 D1).
//
// Why it has to be here: with values spread across Blueprint defaults, the Intent and the
// Assignment, no single layer is a complete spec, so each is validated as PARTIAL where it
// is declared. That leaves exactly one place where "is this spec actually complete and
// well-typed" can be answered — after the merge. `ValidateIntentSpecPartial`'s own doc
// already booked this as a follow-up ("full resolved-spec revalidation at compile is a
// follow-up"); this is it.
//
// A failure skips the one Assignment and is surfaced on the plan (§1.8), never silently
// compiling a Baseline from a spec that does not satisfy its kind.
func validateResolvedSpec(kind string, resolved map[string]any) error {
	raw, err := json.Marshal(resolved)
	if err != nil {
		return fmt.Errorf("marshal resolved spec: %w", err)
	}
	covered, err := contract.ValidateIntentSpec(kind, raw)
	if err != nil {
		return fmt.Errorf("resolved spec (blueprint defaults + intent + assignment values) violates kind %q: %w", kind, err)
	}
	if !covered {
		// Declaration-time validation already rejects an unimplemented kind; reaching
		// here would mean the schema registry changed under a live Assignment.
		return fmt.Errorf("kind %q has no spec schema, so the resolved spec cannot be validated", kind)
	}
	return nil
}

// removeVerb renders the onRemove intent for the orphan-Finding message.
func removeVerb(onRemove string) string {
	if onRemove == types.OnRemoveRevert {
		return "restore prior state"
	}
	return "decommission"
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
	// NotBefore is a duration string (the renewal window) — substitute
	// {{.spec.renewBefore}} explicitly (ADR-0030 threshold; ADR-0024 engine).
	if exp.NotBefore != "" {
		nb, err := template.Substitute(exp.NotBefore, ns)
		if err != nil {
			return exp, err.Error()
		}
		exp.NotBefore, _ = nb.(string)
	}
	if len(exp.Equals) == 0 && len(exp.Contains) == 0 && exp.NotBefore == "" {
		return exp, "observe expectation requires equals, contains, or notBefore"
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

// resolveOwnership decides which Blueprint ownership registrations to perform
// (§2.1). A namespace already owned by a Syncer or team is read-observed — no
// claim. Otherwise EVERY claimant Blueprint is registered as an owner.
//
// Many Blueprints may own one namespace, and that is ADR-0060's model, not a
// relaxation of it. This function used to refuse a second Blueprint claimant
// outright ("one namespace has one write owner"), which was written for the
// ADR-0023 data layer where graph.facet_owner was keyed by namespace ALONE.
// ADR-0060 re-keyed it (namespace, owner_ref) and re-based the §1.2
// single-writer invariant onto (Entity, namespace, source) — in the words of
// migration 00035, dropping "a global per-namespace monopoly that strips
// capability" because the estate-wide lock "added zero per-Entity protection".
// The compiler kept the monopoly the store had abandoned, so three application
// Blueprints converging app.config over DISJOINT host sets — apache, tomcat and
// web-server, which is exactly ADR-0148 D1's one-Blueprint-per-application —
// poisoned each other and the estate would not compile at all.
//
// The protection that actually matters survives, sharper: two EXCLUSIVE claims
// on one (namespace, Entity) still fail the compile in detectClaimConflicts,
// per-Entity and by name (§2.4, exclusive-fails-compile / additive-union). This
// pass registers who MAY write; it is not where contention is decided.
func resolveOwnership(ctx context.Context, s Store, claims []ownClaim, skipped map[string]bool) ([]types.FacetOwner, error) {
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
	namespaces := make([]string, 0, len(byNS))
	for ns := range byNS {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)

	for _, ns := range namespaces {
		blueprints := byNS[ns]
		owner, owned, err := s.GetFacetOwner(ctx, ns)
		if err != nil {
			return nil, err
		}
		if owned && owner.OwnerKind != blueprintOwnerKind {
			continue // Syncer/team-owned: read-only observation, no claim
		}
		// Register every claimant (idempotent if already an owner). Sorted so a
		// Plan is byte-stable across compiles — map order is not.
		bps := make([]string, 0, len(blueprints))
		for bp := range blueprints {
			bps = append(bps, bp)
		}
		sort.Strings(bps)
		for _, bp := range bps {
			owners = append(owners, types.FacetOwner{Namespace: ns, OwnerKind: blueprintOwnerKind, OwnerRef: bp})
		}
	}
	return owners, nil
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

// shortIntentKind strips the "Intent/" prefix — capability maps and binding entries key by the bare
// kind ("Application"), the same convention provisions/decommissions use.
func shortIntentKind(kind string) string { return strings.TrimPrefix(kind, "Intent/") }
