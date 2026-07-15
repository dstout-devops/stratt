// Package webhook is the webhook Actuator (charter §2.3, ADR-0027): the
// outbound notification transport. It runs a single JSON HTTP POST inside an
// execution pod so the delivery URL and any bearer token are injected as
// CredentialRef files at spawn (§2.5) — the control-plane notifier composes
// the pod but never sees the secret material.
//
// The driver is tool content (generated python, executed in the pod), not
// control-plane Python (ADR-0002). It reads the credential from
// /runner/credentials/webhook/{url,token} and the body from
// /runner/project/body, and emits exactly one JSON event carrying the HTTP
// status — never the url, token, or body (§2.5: nothing secret-adjacent
// leaves the pod on the event stream).
package webhook

import (
	"encoding/json"
	"fmt"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/types"
)

// CredentialMountName is the fixed RefName the notifier mounts the Sink's
// CredentialRef under, so the driver reads from a known path
// (/runner/credentials/webhook/…) regardless of the underlying ref name.
const CredentialMountName = "webhook"

// params is the actuator's interpretation of Step params (Contract:
// contracts/actuators/webhook.input.schema.json). url/token are NOT here —
// they arrive as injected credential files (§2.5).
type params struct {
	Body    string            `json:"body"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	// CredentialMount is the credential dir the driver reads url/token from —
	// the CredentialRef name RunAction mounts under (/runner/credentials/<name>/).
	// Empty → the legacy fixed "webhook" mount.
	CredentialMount string `json:"credentialMount"`
}

// driverPy issues one request and prints one event line. It deliberately
// avoids echoing the url/token/body or raw exception text (which can embed the
// URL) — only the HTTP status and a sanitized failure class (§2.5 + §1.8).
const driverPy = `import json, os, sys, urllib.request, urllib.error

base = "/runner/project"
with open(base + "/step.json") as f:
    step = json.load(f)
cred = "/runner/credentials/" + (step.get("credentialMount") or "webhook")
with open(base + "/body") as f:
    body = f.read()
with open(cred + "/url") as f:
    url = f.read().strip()
token = ""
tp = cred + "/token"
if os.path.exists(tp):
    with open(tp) as f:
        token = f.read().strip()

headers = dict(step.get("headers") or {})
headers.setdefault("Content-Type", "application/json")
if token:
    headers["Authorization"] = "Bearer " + token
method = step.get("method") or "POST"

status = 0
ok = False
detail = ""
try:
    req = urllib.request.Request(url, data=body.encode(), method=method, headers=headers)
    resp = urllib.request.urlopen(req, timeout=15)
    status = resp.getcode()
    ok = 200 <= status < 300
    if not ok:
        detail = "http %d" % status
except urllib.error.HTTPError as e:
    status = e.code
    detail = "http %d" % e.code
except Exception as e:
    # NEVER str(e): urllib errors embed the target URL (a secret). Class only.
    detail = type(e).__name__

sys.stdout.write(json.dumps({"counter": 1, "event": "delivery_finished",
    "host": "webhook", "status": status, "ok": ok, "detail": detail}) + "\n")
sys.stdout.flush()
sys.exit(0 if ok else 1)
`

// Actuator implements the webhook Actuator.
type Actuator struct{}

// Name implements actuators.Actuator.
func (Actuator) Name() string { return types.SinkWebhook }

// Prepare renders the driver, body, and step config into pod content. The
// credential (url/token) is projected separately by the dispatcher from the
// CredentialMount the caller supplies (§2.5) — it never appears here.
func (Actuator) Prepare(raw json.RawMessage, _ []actuators.Target) (actuators.JobSpec, error) {
	var p params
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return actuators.JobSpec{}, fmt.Errorf("webhook: invalid params: %w", err)
		}
	}
	if p.Body == "" {
		return actuators.JobSpec{}, fmt.Errorf("webhook: params.body is required")
	}
	switch p.Method {
	case "", "POST":
		p.Method = "POST"
	case "PUT":
	default:
		return actuators.JobSpec{}, fmt.Errorf("webhook: unsupported method %q (POST, PUT)", p.Method)
	}
	stepDoc, err := json.Marshal(map[string]any{"method": p.Method, "headers": p.Headers, "credentialMount": p.CredentialMount})
	if err != nil {
		return actuators.JobSpec{}, fmt.Errorf("webhook: marshal step content: %w", err)
	}
	return actuators.JobSpec{
		Files: map[string]string{
			"project/driver.py": driverPy,
			"project/body":      p.Body,
			"project/step.json": string(stepDoc),
		},
		Command: []string{"python3", "/runner/project/driver.py"},
	}, nil
}

// driverEvent is the one line the driver emits.
type driverEvent struct {
	Counter int64  `json:"counter"`
	Event   string `json:"event"`
	Host    string `json:"host"`
	Status  int    `json:"status"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"`
}

// Interpret maps the driver's delivery event to a task event + terminal
// result.
func (Actuator) Interpret(line []byte) (actuators.Interpreted, bool) {
	var ev driverEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Event == "" {
		return actuators.Interpreted{}, false
	}
	payload := map[string]any{"status": ev.Status}
	if ev.Detail != "" {
		payload["detail"] = ev.Detail
	}
	out := actuators.Interpreted{Event: types.RunEvent{
		Seq:     ev.Counter,
		Kind:    ev.Event,
		Target:  ev.Host,
		Payload: payload,
	}}
	if ev.Event == "delivery_finished" {
		status := actuators.StatusOK
		if !ev.OK {
			status = actuators.StatusFailed
		}
		out.Result = &actuators.TargetResult{Target: ev.Host, Status: status, Failed: !ev.OK}
	}
	return out, true
}
