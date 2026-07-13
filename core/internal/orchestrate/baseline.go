// Baseline check execution (charter §2.4, §4.3, ADR-0019): a Baseline's
// cadence fires RunBaselineCheck, which runs the declared check Step as an
// ordinary child Run — "tofu plan on cron IS drift detection — no special
// case" (§5 Flow 2) — and folds the per-target verdicts into Findings
// through the flap-damping state machine.
package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// BaselineInput starts one cadence fire of a Baseline check.
type BaselineInput struct {
	BaselineName string
}

// RunBaselineCheck loads the Baseline (pinned into workflow state — a Git
// update mid-flight changes future checks, never this one), executes its
// check Step as a child RunAgainstView, and evaluates the outcome into
// Findings. A failed check Run records no observations: a broken check is
// evidence of neither drift nor cleanliness.
func RunBaselineCheck(ctx workflow.Context, in BaselineInput) error {
	opts := workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	ctx = workflow.WithActivityOptions(ctx, opts)
	var a *Activities

	var b types.Baseline
	if err := workflow.ExecuteActivity(ctx, a.LoadBaseline, in.BaselineName).Get(ctx, &b); err != nil {
		return err
	}

	// facet-observation Baselines (compiler output, ADR-0023) are evaluated
	// graph-side — no execution pod. The compiled desired state is "these
	// Entities should carry this Facet value" (§2.4). The Temporal workflow
	// history is the Evidence (Findings carry no check-Run ref).
	if b.Mode == types.FacetObservation {
		var eval graph.ObservationOutcome
		if err := workflow.ExecuteActivity(ctx, a.EvaluateFacetBaseline, b).Get(ctx, &eval); err != nil {
			return err
		}
		return workflow.ExecuteActivity(ctx, a.SealEvidence, b.Name).Get(ctx, nil)
	}

	runIn, err := checkRunInput(b)
	if err != nil {
		return err
	}

	cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID: workflow.GetInfo(ctx).WorkflowExecution.ID + "-run",
	})
	var outcome RunOutcome
	if err := workflow.ExecuteChildWorkflow(cctx, RunAgainstView, runIn).Get(cctx, &outcome); err != nil {
		// The check itself broke (View resolution, dispatch, …): visible on
		// the Run and this workflow's history; Findings stay untouched.
		return err
	}

	var eval graph.ObservationOutcome
	if err := workflow.ExecuteActivity(ctx, a.EvaluateBaseline, b, outcome).Get(ctx, &eval); err != nil {
		return err
	}
	return workflow.ExecuteActivity(ctx, a.SealEvidence, b.Name).Get(ctx, nil)
}

// checkRunInput renders a Baseline's check Step into a RunInput, enforcing —
// structurally, not by convention — that the check cannot mutate: ansible
// runs with check forced on; opentofu only ever in plan mode. Declaration
// validation rejects the same upstream; this is the launch-time guarantee.
func checkRunInput(b types.Baseline) (RunInput, error) {
	params := map[string]any{}
	for k, v := range b.Params {
		params[k] = v
	}
	actuator := b.Actuator
	if actuator == "" {
		actuator = "ansible"
	}
	switch actuator {
	case "ansible":
		params["check"] = true
	case "opentofu":
		if mode, _ := params["mode"].(string); mode != "plan" {
			return RunInput{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("baseline %s: opentofu checks require mode=plan", b.Name), "BaselineNotReadOnly", nil)
		}
	default:
		return RunInput{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("baseline %s: actuator %q has no read-only check semantics (ansible, opentofu)", b.Name, actuator),
			"BaselineNotReadOnly", nil)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return RunInput{}, err
	}
	return RunInput{
		ViewName: b.ViewName, Actuator: actuator, Params: raw, Slices: b.Slices,
		Baseline: b.Name, Principal: b.Principal, CredentialRefs: b.CredentialRefs,
	}, nil
}

// EvaluateFacetBaseline resolves a compiled Baseline's target set and checks
// each Entity's Facets against the expectations (ADR-0023), feeding the same
// §4.3 flap-damping machinery as check-Run Baselines. An Entity is drifted
// if any expectation is unmet (a missing Facet is unmet — desired state
// absent is drift). runID stays empty: the workflow history is the Evidence.
func (a *Activities) EvaluateFacetBaseline(ctx context.Context, b types.Baseline) (graph.ObservationOutcome, error) {
	// Compiler-emitted Baselines (ADR-0023) carry a compiled Selector (the
	// Assignment's View ∩ the Blueprint route). Hand-written facet-observation
	// Baselines (ADR-0033) name a viewName instead — resolve that View to its
	// live Entity set. One of the two is always present (ValidateBaseline
	// requires viewName; the compiler always sets Selector).
	var ents []types.Entity
	var err error
	switch {
	case b.Selector != nil:
		ents, err = a.Store.ResolveSelector(ctx, *b.Selector, nil, 0)
	case b.ViewName != "":
		_, ents, err = a.Store.ResolveView(ctx, b.ViewName, nil, 0)
	default:
		return graph.ObservationOutcome{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("baseline %s: facet-observation requires a selector or viewName", b.Name), "InvalidBaseline", nil)
	}
	if err != nil {
		return graph.ObservationOutcome{}, err
	}
	obs := map[string]graph.BaselineObservation{}
	for _, e := range ents {
		facets, err := a.Store.GetFacets(ctx, e.ID)
		if err != nil {
			return graph.ObservationOutcome{}, err
		}
		byNS := map[string]json.RawMessage{}
		for _, f := range facets {
			byNS[f.Namespace] = f.Value
		}
		var unmet []map[string]any
		for _, exp := range b.Expected {
			if reason := expectationUnmet(byNS[exp.Namespace], exp); reason != "" {
				unmet = append(unmet, map[string]any{
					"namespace": exp.Namespace, "path": exp.Path, "reason": reason,
				})
			}
		}
		o := graph.BaselineObservation{EntityID: e.ID}
		if len(unmet) > 0 {
			o.Drifted = true
			o.Detail, _ = json.Marshal(unmet)
		}
		obs[e.ID] = o
	}
	out, err := a.Store.RecordBaselineObservations(ctx, b, "", obs)
	if err != nil {
		return out, err
	}
	return out, a.emitFindingNotices(ctx, out)
}

// SealEvidence seals an object-locked audit bundle for every open Finding of a
// Baseline that lacks one (§2.4, ADR-0029). Keyed by the manifest's absence, not
// the one-shot pending→open transition, so a seal failure + activity retry
// re-seals the missed Findings (write-once by the graph unique index). A no-op
// when no object store is configured — Findings then open unsealed.
func (a *Activities) SealEvidence(ctx context.Context, baseline string) error {
	if a.Evidence == nil {
		return nil
	}
	findings, err := a.Store.ListUnsealedFindings(ctx, baseline)
	if err != nil {
		return err
	}
	for _, f := range findings {
		bundle, err := a.buildEvidenceBundle(ctx, f)
		if err != nil {
			return err
		}
		sealed, err := a.Evidence.Seal(ctx, "evidence/"+f.ID+".json", bundle)
		if err != nil {
			return err
		}
		rec := types.Evidence{
			FindingID: f.ID, Baseline: f.Baseline, Target: f.Target,
			ObjectKey: sealed.Key, SHA256: sealed.SHA256, SizeBytes: sealed.Size,
			RetainUntil: sealed.RetainUntil,
		}
		if err := a.Store.RecordEvidence(ctx, rec); err != nil {
			if errors.Is(err, graph.ErrConflict) {
				continue // raced another sealer; the object is write-once anyway
			}
			return err
		}
	}
	return nil
}

// buildEvidenceBundle assembles one Finding's audit bundle: the redacted diff +
// Finding metadata, the check Run summary, and a durable snapshot of the Run's
// NATS task-event stream (the one artifact class not otherwise persisted, §3).
func (a *Activities) buildEvidenceBundle(ctx context.Context, f types.Finding) ([]byte, error) {
	bundle := map[string]any{
		"schema":  "stratt.evidence/v1",
		"finding": f,
	}
	if f.RunID != "" {
		if run, err := a.Store.GetRun(ctx, f.RunID); err == nil {
			bundle["run"] = map[string]any{
				"id": run.ID, "status": run.Status, "viewRef": run.ViewRef,
				"startedAt": run.StartedAt, "finishedAt": run.FinishedAt,
			}
		}
		// Snapshot the Run's event stream from NATS, stopping at the stream-end
		// marker FinishRun publishes (bounded so a missing marker can't hang).
		// Durably persisting task events into a long-retained store is safe only
		// under the §2.5 invariant that task events never carry secret material
		// (credentials inject at pod spawn, are never logged) — the bundle relies
		// on that, it does not re-scrub.
		events := []types.RunEvent{}
		tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		_ = a.Bus.Tail(tctx, f.RunID, func(ev types.RunEvent) error {
			events = append(events, ev)
			if ev.Kind == "stream-end" {
				cancel()
			}
			return nil
		})
		bundle["events"] = events
	}
	return json.MarshalIndent(bundle, "", "  ")
}

// emitFindingNotices publishes one finding.open Notice per Finding that
// transitioned to open in this pass (ADR-0027). Only the newly fired ones, so
// re-observation of an already-open Finding stays quiet. NoticeHash dedups the
// publish across activity retries.
func (a *Activities) emitFindingNotices(ctx context.Context, out graph.ObservationOutcome) error {
	for _, f := range out.OpenedFindings {
		n := types.Notice{Kind: types.NoticeFindingOpen, Subject: f.Baseline + "/" + f.Target, Payload: map[string]any{
			"baseline": f.Baseline,
			"target":   f.Target,
			"severity": f.Severity,
		}}
		if f.EntityID != "" {
			n.Payload["entityId"] = f.EntityID
		}
		if f.Framework != "" {
			n.Payload["framework"] = f.Framework
		}
		if err := a.Bus.PublishNotice(ctx, n); err != nil {
			return err
		}
	}
	return nil
}

// expectationUnmet returns "" when the Facet value satisfies the expectation,
// else a short reason. A nil value (Facet absent) is unmet — desired state
// that isn't present is drift.
func expectationUnmet(value json.RawMessage, exp types.FacetExpectation) string {
	if len(value) == 0 {
		return "facet absent"
	}
	at, ok := facetAtPath(value, exp.Path)
	if !ok {
		return "path absent"
	}
	switch {
	case len(exp.Equals) > 0:
		if !jsonEqual(at, exp.Equals) {
			return "value mismatch"
		}
	case len(exp.Contains) > 0:
		if !jsonContains(at, exp.Contains) {
			return "does not contain expected element"
		}
	case exp.NotBefore != "":
		if reason := notBeforeUnmet(at, exp.NotBefore); reason != "" {
			return reason
		}
	}
	return ""
}

// notBeforeUnmet checks the expiry threshold (ADR-0030): the addressed value is
// an RFC3339 timestamp that must be at least `window` (a Go duration) in the
// future at evaluation time. This is a point-in-time observation — `now` is the
// moment of check, stamped by the Run's provenance (§1.8). A malformed value or
// window is unmet (a cert we cannot read the expiry of is drift, not clean).
func notBeforeUnmet(value json.RawMessage, window string) string {
	dur, err := time.ParseDuration(window)
	if err != nil {
		return "invalid renewal window " + strconv.Quote(window)
	}
	var ts string
	if err := json.Unmarshal(value, &ts); err != nil {
		return "value is not a timestamp string"
	}
	notAfter, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "value is not an RFC3339 timestamp"
	}
	if notAfter.Before(time.Now().Add(dur)) {
		return "within renewal window (expires " + ts + ")"
	}
	return ""
}

// facetAtPath walks a dotted path into a JSON document, returning the
// sub-value. "" returns the whole document.
func facetAtPath(doc json.RawMessage, path string) (json.RawMessage, bool) {
	if path == "" {
		return doc, true
	}
	var cur any
	if err := json.Unmarshal(doc, &cur); err != nil {
		return nil, false
	}
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	raw, err := json.Marshal(cur)
	if err != nil {
		return nil, false
	}
	return raw, true
}

// jsonEqual compares two JSON documents by canonical value.
func jsonEqual(a, b json.RawMessage) bool {
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// jsonContains reports whether haystack (a JSON array or string) contains the
// needle element (array membership by value; string substring).
func jsonContains(haystack, needle json.RawMessage) bool {
	var hv any
	if json.Unmarshal(haystack, &hv) != nil {
		return false
	}
	switch h := hv.(type) {
	case []any:
		var nv any
		if json.Unmarshal(needle, &nv) != nil {
			return false
		}
		for _, el := range h {
			if reflect.DeepEqual(el, nv) {
				return true
			}
		}
		return false
	case string:
		var ns string
		if json.Unmarshal(needle, &ns) != nil {
			return false
		}
		return strings.Contains(h, ns)
	default:
		return false
	}
}

// LoadBaseline reads the declared Baseline.
func (a *Activities) LoadBaseline(ctx context.Context, name string) (types.Baseline, error) {
	b, err := a.Store.GetBaseline(ctx, name)
	if err != nil {
		return b, temporal.NewNonRetryableApplicationError(err.Error(), "BaselineNotFound", err)
	}
	return b, nil
}

// EvaluateBaseline folds one check Run's per-target verdicts into the
// Baseline's Findings (§4.3 flap damping): changed = drifted, ok = clean,
// failed/unreachable = no observation.
func (a *Activities) EvaluateBaseline(ctx context.Context, b types.Baseline, outcome RunOutcome) (graph.ObservationOutcome, error) {
	out, err := a.Store.RecordBaselineObservations(ctx, b, outcome.RunID, observationsFromOutcome(outcome))
	if err != nil {
		return out, err
	}
	return out, a.emitFindingNotices(ctx, out)
}

// observationsFromOutcome maps per-target check statuses to observations:
// changed = drifted, ok = clean, failed/unreachable = no observation.
func observationsFromOutcome(outcome RunOutcome) map[string]graph.BaselineObservation {
	obs := map[string]graph.BaselineObservation{}
	for target, status := range outcome.PerTarget {
		o := graph.BaselineObservation{EntityID: outcome.EntityByTarget[target]}
		switch status {
		case actuators.StatusChanged:
			o.Drifted = true
			if fragments := outcome.Drift[target]; len(fragments) > 0 {
				if detail, err := json.Marshal(fragments); err == nil {
					o.Detail = detail
				}
			}
		case actuators.StatusOK:
			// clean observation
		default:
			continue // failed/unreachable: evidence of neither
		}
		obs[target] = o
	}
	return obs
}
