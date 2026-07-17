package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	"go.temporal.io/sdk/testsuite"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/types"
)

func TestCheckRunInputReadOnly(t *testing.T) {
	// A baseline is read-only by platform INVARIANT: DryRun is forced for ANY
	// actuator, and the spine no longer switches on tool name nor writes a
	// tool-specific params.check (ADR-0046 — content-blind).
	in, err := checkRunInput(types.Baseline{Name: "b", ViewName: "v", Cron: "@hourly", Severity: "info"})
	if err != nil {
		t.Fatal(err)
	}
	if in.Actuator != "ansible" || in.Baseline != "b" || !in.DryRun {
		t.Fatalf("baseline must force DryRun read-only via the port bit: %+v", in)
	}
	var p map[string]any
	if err := json.Unmarshal(in.Params, &p); err != nil {
		t.Fatalf("params: %v", err)
	}
	if _, wrote := p["check"]; wrote {
		t.Fatalf("the spine must NOT write a tool-specific params.check (content-blind): %s", in.Params)
	}

	// opentofu: same forced read-only, even when the declaration asks to apply.
	tf, err := checkRunInput(types.Baseline{Name: "b", Actuator: "opentofu",
		Params: map[string]any{"mode": "apply", "module": "m", "workspace": "w"}})
	if err != nil || !tf.DryRun {
		t.Fatalf("a tofu baseline is always forced to a read-only plan: dryRun=%v err=%v", tf.DryRun, err)
	}

	// An actuator that can't run read-only is NO LONGER refused here (no name switch):
	// checkRunInput forces DryRun; the DryRunnable capability gate at LAUNCH rejects it.
	sc, err := checkRunInput(types.Baseline{Name: "b", Actuator: "script"})
	if err != nil || !sc.DryRun {
		t.Fatalf("checkRunInput is content-blind — forces DryRun, defers the capability check to launch: %+v %v", sc, err)
	}
}

// effectfulActuator is a minimal in-tree Actuator (like script/webhook) — it declares
// no read-only capability, so a dry-run must be rejected at launch.
type effectfulActuator struct{}

func (effectfulActuator) Name() string { return "script" }
func (effectfulActuator) Prepare(json.RawMessage, []actuators.Target) (actuators.JobSpec, error) {
	return actuators.JobSpec{}, nil
}
func (effectfulActuator) Interpret([]byte) (actuators.Interpreted, bool) {
	return actuators.Interpreted{}, false
}

// TestExecute_InTreeActuatorRejectsDryRun proves the launch-time capability gate the
// baseline read-only path now relies on (ADR-0046): an effectful in-tree pod Actuator
// can never run a dry-run, so it fails visibly rather than silently running live.
func TestExecute_InTreeActuatorRejectsDryRun(t *testing.T) {
	a := &Activities{Actuators: map[string]actuators.Actuator{"script": effectfulActuator{}}}
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)
	_, err := env.ExecuteActivity(a.Execute,
		RunInput{Actuator: "script", DryRun: true}, 0, "", ResolvedTargets{}, []dispatch.CredentialMount(nil))
	if err == nil || !strings.Contains(err.Error(), "does not support dry-run") {
		t.Fatalf("a dry-run through an effectful in-tree actuator must be rejected at launch, got %v", err)
	}
}

func TestObservationsFromOutcome(t *testing.T) {
	outcome := RunOutcome{
		RunID: "r1",
		PerTarget: map[string]string{
			"vm-1": actuators.StatusChanged,
			"vm-2": actuators.StatusOK,
			"vm-3": actuators.StatusFailed,
			"vm-4": actuators.StatusUnreachable,
		},
		EntityByTarget: map[string]string{"vm-1": "ent-1"},
		Drift:          map[string][]json.RawMessage{"vm-1": {json.RawMessage(`{"task":"sysctl"}`)}},
	}
	obs := observationsFromOutcome(outcome)
	if len(obs) != 2 {
		t.Fatalf("failed/unreachable must not observe: %+v", obs)
	}
	if o := obs["vm-1"]; !o.Drifted || o.EntityID != "ent-1" || len(o.Detail) == 0 {
		t.Fatalf("vm-1: %+v", o)
	}
	if o := obs["vm-2"]; o.Drifted || len(o.Detail) != 0 {
		t.Fatalf("vm-2 must be a clean observation: %+v", o)
	}
}
