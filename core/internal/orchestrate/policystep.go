package orchestrate

import (
	"context"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"

	"github.com/dstout-devops/stratt/core/internal/policy"
	"github.com/dstout-devops/stratt/types"
)

// EvaluatePolicy runs the built-in PDP over the assembled ChangeContext
// (ADR-0063 / ADR-0062). It lives in an activity because CEL compilation and
// decision timestamping are non-deterministic and must not run on the workflow
// goroutine. It logs the decision so the Intent → Run → step descent can
// explain the outcome (§1.8); durable Finding/Evidence recording is §7.2d.
func (a *Activities) EvaluatePolicy(ctx context.Context, controls []types.Control, cc types.ChangeContext) (types.Decision, error) {
	dec := policy.Evaluate(controls, cc)
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
	return dec, nil
}

// runPolicyStep evaluates a policy checkpoint synchronously and maps the
// four-way outcome to a Step status (ADR-0063). The ChangeContext is assembled
// deterministically on the workflow goroutine; only the evaluation crosses into
// the activity. A failed evaluation is fail-closed (the step fails, never a
// silent pass).
func runPolicyStep(ctx workflow.Context, a *Activities, in DAGInput, step types.Step) string {
	cc := assembleChangeContext(in)
	var dec types.Decision
	if err := workflow.ExecuteActivity(ctx, a.EvaluatePolicy, step.Policy.Controls, cc).Get(ctx, &dec); err != nil {
		return stepFailed
	}
	return PolicyStepStatus(dec.Outcome)
}

// PolicyStepStatus maps a PDP outcome to a DAG step status (ADR-0063 v1): allow
// succeeds; deny / require_approval / escalate fail closed. require_approval and
// escalate BLOCK until the interactive Gate wiring of §7.2b — never a silent
// pass. Pure, so it is unit-tested directly.
func PolicyStepStatus(outcome string) string {
	if outcome == types.OutcomeAllow {
		return stepSucceeded
	}
	return stepFailed
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
