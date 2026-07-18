package orchestrate

import (
	"context"
	"encoding/json"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"

	"github.com/dstout-devops/stratt/core/internal/policy"
	"github.com/dstout-devops/stratt/types"
)

// PolicyEvalArg is the activity input for one policy checkpoint: the controls,
// the assembled ChangeContext, and the run/step the decision belongs to (so the
// durable audit record can reference them, ADR-0065).
type PolicyEvalArg struct {
	WorkflowRunID string
	StepName      string
	Controls      []types.Control
	Context       types.ChangeContext
}

// EvaluatePolicy runs the built-in PDP over the assembled ChangeContext
// (ADR-0063 / ADR-0062). It lives in an activity because CEL compilation,
// decision timestamping, and the audit write are non-deterministic and must not
// run on the workflow goroutine. Every decision is recorded on the one
// hash-chained audit stream (ADR-0065) and logged, so the Intent → Run → step
// descent can explain the outcome (§1.8).
func (a *Activities) EvaluatePolicy(ctx context.Context, arg PolicyEvalArg) (types.Decision, error) {
	dec := policy.Evaluate(arg.Controls, arg.Context)
	codes := make([]string, 0, len(dec.Reasons))
	for _, r := range dec.Reasons {
		codes = append(codes, r.Code+":"+r.ControlID)
	}
	lg := activity.GetLogger(ctx)
	if dec.Outcome == types.OutcomeAllow {
		lg.Info("policy decision", "outcome", dec.Outcome, "reasons", codes)
	} else {
		lg.Warn("policy decision blocked", "outcome", dec.Outcome, "reasons", codes)
	}
	// Durable, tamper-evident record on the audit chain (ADR-0065). A failed
	// audit write is visible, never swallowed (§1.8); nil-guarded like Evidence.
	if a.Store != nil {
		if err := a.Store.RecordAudit(context.WithoutCancel(ctx), policyAuditEvent(arg, dec)); err != nil {
			lg.Error("policy decision audit failed", "err", err)
		}
	}
	return dec, nil
}

// policyAuditEvent maps a decision to its audit record (ADR-0065): outcome →
// audit outcome (allow=ok, deny=denied, require_approval/escalate verbatim);
// reasons + step ride in Detail (structured, never secret — §2.5). Pure, so it
// is unit-tested directly.
func policyAuditEvent(arg PolicyEvalArg, dec types.Decision) types.AuditEvent {
	outcome := types.AuditOK
	switch dec.Outcome {
	case types.OutcomeDeny:
		outcome = types.AuditDenied
	case types.OutcomeRequireApproval, types.OutcomeEscalate:
		outcome = dec.Outcome
	}
	detail, _ := json.Marshal(struct {
		Outcome string         `json:"outcome"`
		Step    string         `json:"step"`
		Reasons []types.Reason `json:"reasons,omitempty"`
	}{dec.Outcome, arg.StepName, dec.Reasons})
	return types.AuditEvent{
		PrincipalID: arg.Context.Actor.ID,
		Action:      types.AuditPolicyDecision,
		Object:      arg.WorkflowRunID,
		Outcome:     outcome,
		Detail:      detail,
	}
}

// runPolicyStep evaluates a policy checkpoint synchronously and gates the DAG on
// the four-way outcome (ADR-0063 / ADR-0064). The ChangeContext is assembled
// deterministically on the workflow goroutine; only the evaluation crosses into
// the activity. allow succeeds; deny fails; require_approval/escalate OPEN a
// human Gate whose approvers come from the decision's obligation (ADR-0064) —
// with no approver obligation, an approval is unsatisfiable and fails closed
// (§1.8: never a silent pass). A failed evaluation also fails closed.
func runPolicyStep(ctx workflow.Context, a *Activities, in DAGInput, step types.Step) string {
	arg := PolicyEvalArg{
		WorkflowRunID: in.WorkflowRunID,
		StepName:      step.Name,
		Controls:      step.Policy.Controls,
		Context:       assembleChangeContext(in),
	}
	var dec types.Decision
	if err := workflow.ExecuteActivity(ctx, a.EvaluatePolicy, arg).Get(ctx, &dec); err != nil {
		return stepFailed
	}
	switch dec.Outcome {
	case types.OutcomeRequireApproval, types.OutcomeEscalate:
		approvers, timeout, ok := approversFromDecision(dec)
		if !ok {
			return stepFailed // an approval with no approver obligation is unsatisfiable
		}
		return awaitGate(ctx, a, in.WorkflowRunID, step.Name, approvers, timeout, "")
	default:
		return PolicyStepStatus(dec.Outcome)
	}
}

// PolicyStepStatus is the terminal (non-Gate) outcome→status mapping: allow
// succeeds, everything else fails closed. runPolicyStep routes
// require_approval/escalate to a Gate first; this is the deny path and the
// fail-closed fallback. Pure, so it is unit-tested directly.
func PolicyStepStatus(outcome string) string {
	if outcome == types.OutcomeAllow {
		return stepSucceeded
	}
	return stepFailed
}

// approversFromDecision extracts the Gate approvers a require_approval/escalate
// decision carries (ADR-0064): the require_approval obligation's params.teams /
// params.principals become the GateApprovers, params.timeoutSeconds its timeout.
// ok=false when no approver is named (the caller fails closed). M-of-N quorum
// (params.count > 1) is not enforced here — that is the §7.3 Quorum control.
func approversFromDecision(dec types.Decision) (types.GateApprovers, int, bool) {
	var ap types.GateApprovers
	timeout := 0
	for _, ob := range dec.Obligations {
		if ob.Type != types.ObligationRequireApproval {
			continue
		}
		ap.Teams = append(ap.Teams, paramStrings(ob.Params, "teams")...)
		ap.Principals = append(ap.Principals, paramStrings(ob.Params, "principals")...)
		if t, ok := paramInt(ob.Params, "timeoutSeconds"); ok {
			timeout = t
		}
	}
	ok := len(ap.Teams) > 0 || len(ap.Principals) > 0
	return ap, timeout, ok
}

// paramStrings reads a []string obligation param, tolerating both a native
// []string (Go-constructed) and a []any of strings (JSON-decoded from estate).
func paramStrings(params map[string]any, key string) []string {
	switch v := params[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// paramInt reads an int obligation param, tolerating float64 (JSON) and int.
func paramInt(params map[string]any, key string) (int, bool) {
	switch v := params[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}

// assembleChangeContext builds the typed ChangeContext a policy Step evaluates
// from the run's DAGInput (ADR-0063). It is deterministic (no I/O, no clock), so
// it is safe on the workflow goroutine. v1 surfaces the launching Principal as
// the actor and the launch inputs as labels + environment; richer enrichment
// (blast-radius from View membership, per-target criticality) is sparse and
// fail-safe (ADR-0061 M4) and lands with §7.6.
func assembleChangeContext(in DAGInput) types.ChangeContext {
	cc := types.ChangeContext{
		Actor:  types.PrincipalRef{ID: in.Principal},
		Labels: map[string]string{},
	}
	for k, v := range in.LaunchParams {
		if s, ok := v.(string); ok {
			cc.Labels[k] = s
		}
	}
	if env, ok := cc.Labels["environment"]; ok {
		cc.Environment = env
	}
	return cc
}
