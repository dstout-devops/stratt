// Package opentofu is the OpenTofu Actuator (charter §2.3, §3: OpenTofu over
// Terraform; §8 Phase 2). plan renders and streams a plan; apply mutates —
// and belongs behind a Gate (ADR-0016: the plan → Gate → apply Workflow is
// the intended shape). State lives in strattd's encrypted HTTP backend.
//
// The driver is tool content (python in the EE pod, ADR-0002) wrapping
// tofu's machine-readable -json stream with deterministic counters so retry
// re-publishes dedup server-side.
package opentofu

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/types"
)

// params is the actuator's interpretation of Step params — an internal
// convenience; the Contract is contracts/actuators/opentofu.input (§1.5).
type params struct {
	Module    string         `json:"module"`
	Mode      string         `json:"mode"`
	Workspace string         `json:"workspace"`
	Vars      map[string]any `json:"vars"`
	EEImage   string         `json:"eeImage"`
}

// CredentialFunc derives the per-workspace state-backend credential; wired
// from the statebackend at startup so Prepare stays free of key material
// handling (it receives only the derived, workspace-scoped credential).
type CredentialFunc func(workspace string) string

// Actuator implements the opentofu Actuator.
type Actuator struct {
	// BackendURL is the state-backend base URL as execution pods reach it
	// (STRATT_STATE_BACKEND_URL). Empty disables Prepare — never plaintext
	// local state by accident.
	BackendURL string
	// Credential derives TF_HTTP_PASSWORD per workspace.
	Credential CredentialFunc
	// DefaultImage is the tofu EE image (STRATT_EE_TOFU_IMAGE).
	DefaultImage string
}

// Name implements actuators.Actuator.
func (Actuator) Name() string { return "opentofu" }

// driverPy runs tofu and wraps each -json line as {"counter": n, "tofu": {…}}.
// Driver-level events (init failures, phase markers, final rc) use the same
// envelope with "event" instead of "tofu".
const driverPy = `import json, subprocess, sys

base = "/runner/project"
with open(base + "/step.json") as f:
    step = json.load(f)
mode = step["mode"]

counter = 0
def emit(**kw):
    global counter
    counter += 1
    kw["counter"] = counter
    sys.stdout.write(json.dumps(kw) + "\n")
    sys.stdout.flush()

def run(args, stream=True):
    """Run tofu, wrapping each stdout line; returns the exit code."""
    emit(event="phase", phase=" ".join(args[:2]))
    proc = subprocess.Popen(args, cwd=base, stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT, text=True)
    for line in proc.stdout:
        line = line.rstrip("\n")
        if not line:
            continue
        try:
            emit(tofu=json.loads(line))
        except ValueError:
            emit(event="raw", line=line)
    return proc.wait()

rc = run(["tofu", "init", "-input=false", "-no-color", "-json"])
if rc == 0:
    if mode == "plan":
        rc = run(["tofu", "plan", "-input=false", "-json", "-out=/runner/artifacts/plan.tfplan"])
        if rc == 0:
            show = subprocess.run(["tofu", "show", "-json", "/runner/artifacts/plan.tfplan"],
                                  cwd=base, capture_output=True, text=True)
            if show.returncode == 0:
                emit(event="plan_json", plan=json.loads(show.stdout))
            else:
                emit(event="raw", line=show.stderr.strip())
                rc = show.returncode
    else:
        rc = run(["tofu", "apply", "-input=false", "-auto-approve", "-json"])
        if rc == 0:
            outp = subprocess.run(["tofu", "output", "-json"],
                                  cwd=base, capture_output=True, text=True)
            if outp.returncode == 0:
                outputs = json.loads(outp.stdout or "{}")
                # Redact sensitive values BEFORE emission: the event stream
                # is not a secret channel (ADR-0017).
                for name, o in outputs.items():
                    if o.get("sensitive"):
                        o["value"] = "(sensitive)"
                emit(event="outputs_json", outputs=outputs)
            else:
                emit(event="raw", line=outp.stderr.strip())
                rc = outp.returncode

emit(event="tofu_finished", rc=rc, mode=mode)
sys.exit(1 if rc else 0)
`

// backendTF renders the http backend block. The credential rides pod env
// (TF_HTTP_PASSWORD), never files (§2.5 posture).
const backendTF = `terraform {
  backend "http" {
    address        = %q
    lock_address   = %q
    unlock_address = %q
    lock_method    = "LOCK"
    unlock_method  = "UNLOCK"
    username       = "stratt"
  }
}
`

// Prepare implements actuators.Actuator.
func (a Actuator) Prepare(raw json.RawMessage, _ []actuators.Target) (actuators.JobSpec, error) {
	if a.BackendURL == "" || a.Credential == nil {
		return actuators.JobSpec{}, fmt.Errorf("opentofu: state backend not configured (STRATT_STATE_KEY / STRATT_STATE_BACKEND_URL) — refusing to run without encrypted remote state (ADR-0016)")
	}
	var p params
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return actuators.JobSpec{}, fmt.Errorf("opentofu: invalid params: %w", err)
		}
	}
	// Defense-in-depth behind the Contract seam (§1.5 validates upstream).
	if p.Module == "" || p.Workspace == "" || (p.Mode != "plan" && p.Mode != "apply") {
		return actuators.JobSpec{}, fmt.Errorf("opentofu: module, workspace, and mode (plan|apply) are required")
	}

	stateURL := a.BackendURL + "/statebackend/" + p.Workspace
	stepDoc, err := json.Marshal(map[string]any{"mode": p.Mode})
	if err != nil {
		return actuators.JobSpec{}, err
	}
	files := map[string]string{
		"project/driver.py":  driverPy,
		"project/main.tf":    p.Module,
		"project/backend.tf": fmt.Sprintf(backendTF, stateURL, stateURL, stateURL),
		"project/step.json":  string(stepDoc),
	}
	if len(p.Vars) > 0 {
		vars, err := json.Marshal(p.Vars)
		if err != nil {
			return actuators.JobSpec{}, fmt.Errorf("opentofu: marshal vars: %w", err)
		}
		files["project/stratt.auto.tfvars.json"] = string(vars)
	}

	image := p.EEImage
	if image == "" {
		image = a.DefaultImage
	}
	return actuators.JobSpec{
		Files:   files,
		Command: []string{"python3", "/runner/project/driver.py"},
		Image:   image,
		Env: map[string]string{
			"TF_HTTP_PASSWORD": a.Credential(p.Workspace),
			// tofu writes .terraform under the module dir; keep caches in
			// the pod-writable workdir.
			"TF_DATA_DIR": "/runner/artifacts/.terraform",
		},
	}, nil
}

// driverEvent is one wrapped line of the driver's stream.
type driverEvent struct {
	Counter int64           `json:"counter"`
	Event   string          `json:"event,omitempty"`
	Phase   string          `json:"phase,omitempty"`
	Line    string          `json:"line,omitempty"`
	RC      *int            `json:"rc,omitempty"`
	Mode    string          `json:"mode,omitempty"`
	Plan    json.RawMessage `json:"plan,omitempty"`
	Tofu    json.RawMessage `json:"tofu,omitempty"`
	Outputs json.RawMessage `json:"outputs,omitempty"`
}

// tofuMsg is the subset of tofu's machine-readable stream we lift into
// event kinds; everything else passes through as kind "tofu".
type tofuMsg struct {
	Type       string `json:"type"`
	Message    string `json:"@message"`
	Level      string `json:"@level"`
	Diagnostic *struct {
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
		Detail   string `json:"detail"`
	} `json:"diagnostic"`
	Changes *struct {
		Add       int    `json:"add"`
		Change    int    `json:"change"`
		Remove    int    `json:"remove"`
		Operation string `json:"operation"`
	} `json:"changes"`
	Change *struct {
		Resource struct {
			Addr string `json:"addr"`
		} `json:"resource"`
		Action string `json:"action"`
	} `json:"change"`
}

// Interpret implements actuators.Actuator.
func (Actuator) Interpret(line []byte) (actuators.Interpreted, bool) {
	var ev driverEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Counter == 0 {
		return actuators.Interpreted{}, false
	}
	out := actuators.Interpreted{Event: types.RunEvent{Seq: ev.Counter, Target: "workspace"}}

	switch {
	case len(ev.Tofu) > 0:
		var m tofuMsg
		_ = json.Unmarshal(ev.Tofu, &m)
		payload := map[string]any{"message": m.Message}
		kind := "tofu"
		switch {
		case m.Diagnostic != nil:
			kind = "diagnostic"
			payload["severity"] = m.Diagnostic.Severity
			payload["summary"] = m.Diagnostic.Summary
			if m.Diagnostic.Detail != "" {
				payload["detail"] = m.Diagnostic.Detail
			}
		case m.Type == "planned_change" || m.Type == "apply_start" || m.Type == "apply_complete" ||
			m.Type == "change_summary" || m.Type == "outputs" || m.Type == "resource_drift":
			kind = m.Type
			if m.Changes != nil {
				payload["add"] = m.Changes.Add
				payload["change"] = m.Changes.Change
				payload["remove"] = m.Changes.Remove
			}
			// Drift capture (ADR-0019): a plan that would change IS "changed"
			// in check semantics — "tofu plan on cron is drift detection, no
			// special case" (§5 Flow 2). The plan's change_summary escalates
			// the workspace to changed (statuses only escalate, so the later
			// rc=0 terminal ok cannot hide it); each planned/drifted resource
			// rides along as an observed-vs-expected fragment. Addresses and
			// counts only — never planned values (those stay in the redacted
			// plan-json event).
			switch {
			case m.Type == "change_summary" && m.Changes != nil && m.Changes.Operation == "plan":
				if m.Changes.Add+m.Changes.Change+m.Changes.Remove > 0 {
					out.Result = &actuators.TargetResult{Target: "workspace", Status: actuators.StatusChanged}
					out.Drift, _ = json.Marshal(map[string]any{
						"add": m.Changes.Add, "change": m.Changes.Change, "remove": m.Changes.Remove,
					})
				}
			case (m.Type == "planned_change" || m.Type == "resource_drift") && m.Change != nil:
				fragment := map[string]any{"address": m.Change.Resource.Addr, "action": m.Change.Action}
				if m.Type == "resource_drift" {
					fragment["drift"] = true
				}
				out.Drift, _ = json.Marshal(fragment)
			}
		}
		out.Event.Kind = kind
		out.Event.Payload = payload

	case ev.Event == "plan_json":
		out.Event.Kind = "plan-json"
		// Sensitive planned values are only FLAGGED in tofu's plan JSON
		// (after_sensitive / sensitive_values), not omitted — redact the
		// marked leaves before the event stream sees them (§2.5,
		// charter-guardian F2 on ADR-0017).
		out.Event.Payload = map[string]any{"plan": redactPlan(ev.Plan)}

	case ev.Event == "outputs_json":
		out.Event.Kind = "outputs-json"
		out.Event.Payload = map[string]any{"outputs": json.RawMessage(ev.Outputs)}
		obs, doc, err := interpretOutputs(ev.Outputs)
		if err != nil {
			// A malformed reserved output fails the Run with the pointer
			// detail visible (§1.8); statuses only escalate, so the later
			// rc=0 terminal cannot hide it.
			out.Event.Kind = "invalid-entities"
			out.Event.Payload = map[string]any{"error": err.Error()}
			out.Result = &actuators.TargetResult{Target: "workspace", Status: actuators.StatusFailed, Failed: true}
			return out, true
		}
		out.Entities = obs
		out.OutputsContract = doc

	case ev.Event == "tofu_finished":
		out.Event.Kind = "tofu-finished"
		rc := 0
		if ev.RC != nil {
			rc = *ev.RC
		}
		out.Event.Payload = map[string]any{"rc": rc, "mode": ev.Mode}
		status := actuators.StatusOK
		if ev.Mode == "apply" && rc == 0 {
			status = actuators.StatusChanged
		}
		if rc != 0 {
			status = actuators.StatusFailed
		}
		out.Result = &actuators.TargetResult{Target: "workspace", Status: status, Failed: rc != 0}

	case ev.Event == "phase":
		out.Event.Kind = "phase"
		out.Event.Payload = map[string]any{"phase": ev.Phase}

	case ev.Event == "raw":
		out.Event.Kind = "raw"
		out.Event.Payload = map[string]any{"line": ev.Line}

	default:
		return actuators.Interpreted{}, false
	}
	return out, true
}

// FromEnv builds the Actuator from process configuration (strattd wiring).
func FromEnv(credential CredentialFunc) Actuator {
	image := os.Getenv("STRATT_EE_TOFU_IMAGE")
	if image == "" {
		image = "stratt-ee-tofu:dev"
	}
	return Actuator{
		BackendURL:   os.Getenv("STRATT_STATE_BACKEND_URL"),
		Credential:   credential,
		DefaultImage: image,
	}
}
