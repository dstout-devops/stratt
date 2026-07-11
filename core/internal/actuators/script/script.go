// Package script is the script Actuator (charter §2.3, §8 Phase 1): it runs a
// user-supplied script once per target inside the EE pod and interprets the
// driver's JSON event stream.
//
// The driver is tool content (generated python, executed in the pod) — not
// control-plane Python (ADR-0002: Python lives only in execution pods and the
// plugin SDK). Python rather than shell because the event stream is JSON and
// shell escaping of arbitrary script output is fragile.
package script

import (
	"encoding/json"
	"fmt"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/types"
)

// params is the actuator's interpretation of Step params. This struct is an
// internal convenience, not the Contract — the pinned JSON-Schema Contract
// document for script Steps lands with the Phase-2 Contract machinery (§1.5).
type params struct {
	// Script is the script source, required.
	Script string `json:"script"`
	// Interpreter runs the script: "sh" (default) or "python3" — the two
	// runtimes the EE image guarantees.
	Interpreter string `json:"interpreter"`
}

// driverPy runs the script once per target, sequentially, and emits one JSON
// event per stdout line: target_started, target_output (per script output
// line, stdout and stderr), target_finished (with rc). Counters are
// deterministic so retry re-publishes dedup server-side (events MsgID).
// Exits non-zero if any target failed — a failed target fails the Run,
// matching the ansible Actuator's semantics.
const driverPy = `import json, os, subprocess, sys

def emit(counter, event, host, **kw):
    kw.update({"counter": counter, "event": event, "host": host})
    sys.stdout.write(json.dumps(kw) + "\n")
    sys.stdout.flush()

base = sys.argv[1] if len(sys.argv) > 1 else "/runner/project"
with open(base + "/step.json") as f:
    step = json.load(f)
targets = step["targets"]
interpreter = step.get("interpreter", "sh")

counter = 0
failed = 0
for t in targets:
    counter += 1
    emit(counter, "target_started", t["name"])
    env = dict(os.environ)
    env["STRATT_TARGET_NAME"] = t["name"]
    env["STRATT_ENTITY_ID"] = t["entityId"]
    for k, v in t.get("vars", {}).items():
        env["STRATT_VAR_" + k.upper().replace(".", "_").replace("-", "_")] = v
    proc = subprocess.run(
        [interpreter, base + "/script"],
        env=env, capture_output=True, text=True,
    )
    for stream, text in (("stdout", proc.stdout), ("stderr", proc.stderr)):
        for line in text.splitlines():
            counter += 1
            emit(counter, "target_output", t["name"], stream=stream, line=line)
    counter += 1
    emit(counter, "target_finished", t["name"], rc=proc.returncode)
    if proc.returncode != 0:
        failed += 1

sys.exit(1 if failed else 0)
`

// Actuator implements the script Actuator.
type Actuator struct{}

// Name implements actuators.Actuator.
func (Actuator) Name() string { return "script" }

// Prepare implements actuators.Actuator: render the driver, the script, and
// the targets file into the pod content.
func (Actuator) Prepare(raw json.RawMessage, targets []actuators.Target) (actuators.JobSpec, error) {
	var p params
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return actuators.JobSpec{}, fmt.Errorf("script: invalid params: %w", err)
		}
	}
	if p.Script == "" {
		return actuators.JobSpec{}, fmt.Errorf("script: params.script is required")
	}
	switch p.Interpreter {
	case "":
		p.Interpreter = "sh"
	case "sh", "python3":
	default:
		return actuators.JobSpec{}, fmt.Errorf("script: unsupported interpreter %q (sh, python3)", p.Interpreter)
	}

	type wireTarget struct {
		Name     string            `json:"name"`
		EntityID string            `json:"entityId"`
		Vars     map[string]string `json:"vars,omitempty"`
	}
	wire := make([]wireTarget, len(targets))
	for i, t := range targets {
		wire[i] = wireTarget{Name: t.Name, EntityID: t.EntityID, Vars: t.Vars}
	}
	stepDoc, err := json.Marshal(map[string]any{
		"interpreter": p.Interpreter,
		"targets":     wire,
	})
	if err != nil {
		return actuators.JobSpec{}, fmt.Errorf("script: marshal step content: %w", err)
	}

	return actuators.JobSpec{
		Files: map[string]string{
			"project/driver.py": driverPy,
			"project/script":    p.Script,
			"project/step.json": string(stepDoc),
		},
		Command: []string{"python3", "/runner/project/driver.py"},
	}, nil
}

// driverEvent is one line of the driver's stream.
type driverEvent struct {
	Counter int64  `json:"counter"`
	Event   string `json:"event"`
	Host    string `json:"host"`
	Stream  string `json:"stream,omitempty"`
	Line    string `json:"line,omitempty"`
	RC      *int   `json:"rc,omitempty"`
}

// Interpret implements actuators.Actuator.
func (Actuator) Interpret(line []byte) (actuators.Interpreted, bool) {
	var ev driverEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Event == "" {
		return actuators.Interpreted{}, false
	}
	payload := map[string]any{}
	if ev.Stream != "" {
		payload["stream"] = ev.Stream
		payload["line"] = ev.Line
	}
	if ev.RC != nil {
		payload["rc"] = *ev.RC
	}
	out := actuators.Interpreted{Event: types.RunEvent{
		Seq:     ev.Counter,
		Kind:    ev.Event,
		Target:  ev.Host,
		Payload: payload,
	}}
	if ev.Event == "target_finished" && ev.RC != nil {
		status := actuators.StatusOK
		if *ev.RC != 0 {
			status = actuators.StatusFailed
		}
		out.Result = &actuators.TargetResult{Target: ev.Host, Status: status, Failed: *ev.RC != 0}
	}
	return out, true
}
