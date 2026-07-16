package orchestrate

import (
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/types"
)

func TestCheckRunInputReadOnly(t *testing.T) {
	// ansible: check is forced on, whatever the declaration said.
	in, err := checkRunInput(types.Baseline{Name: "b", ViewName: "v", Cron: "@hourly", Severity: "info"})
	if err != nil {
		t.Fatal(err)
	}
	if in.Actuator != "ansible" || in.Baseline != "b" {
		t.Fatalf("input: %+v", in)
	}
	var p map[string]any
	if err := json.Unmarshal(in.Params, &p); err != nil || p["check"] != true {
		t.Fatalf("check must be forced: %s (%v)", in.Params, err)
	}

	// opentofu: read-only (DryRun plan) is platform-FORCED, whatever the
	// declaration said — a baseline can never converge (ADR-0047 over the port).
	tf, err := checkRunInput(types.Baseline{Name: "b", Actuator: "opentofu",
		Params: map[string]any{"module": "m", "workspace": "w"}})
	if err != nil {
		t.Fatalf("tofu baseline check: %v", err)
	}
	if !tf.DryRun {
		t.Fatal("opentofu baseline must force DryRun (read-only, platform-forced)")
	}
	// Even a declaration asking to apply is forced read-only — never refused, never
	// converging (the platform overrides, mirroring ansible's forced check).
	tf2, err := checkRunInput(types.Baseline{Name: "b", Actuator: "opentofu",
		Params: map[string]any{"mode": "apply", "module": "m", "workspace": "w"}})
	if err != nil || !tf2.DryRun {
		t.Fatalf("a tofu baseline is always forced to a read-only plan: dryRun=%v err=%v", tf2.DryRun, err)
	}
	if _, err := checkRunInput(types.Baseline{Name: "b", Actuator: "script"}); err == nil {
		t.Fatalf("actuator without check semantics must be refused at launch")
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
