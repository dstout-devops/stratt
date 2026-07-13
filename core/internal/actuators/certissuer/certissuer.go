// Package certissuer is the cert-issuer Actuator (charter §2.3, §2.4
// Intent/Certificate GA, ADR-0030): the write side of certificate lifecycle —
// issue / renew / revoke against a Vault-compatible PKI CLM (dev: OpenBao).
//
// Like the webhook Actuator (ADR-0027), it runs inside an execution pod so the
// CLM token is injected as a CredentialRef file at spawn (§2.5) — the control
// plane composes the pod but never holds the secret. The driver reads the token
// from /runner/credentials/cert-issuer/token (the Step's CredentialRef must be
// named "cert-issuer"); addr/role/etc. are non-secret params. It emits exactly
// one event carrying serial numbers — never the token or the issued private key.
//
// Vocabulary note (ADR-0030): the charter names revoke-cert an *Action* (§2.2),
// but no Action-execution framework exists yet; modeling it as an Actuator
// follows the accepted webhook precedent, a conscious deferral, not drift.
package certissuer

import (
	"encoding/json"
	"fmt"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/types"
)

// CredentialMountName is the fixed RefName a cert-issuer Step's CredentialRef
// must use, so the driver reads /runner/credentials/cert-issuer/token
// regardless of the underlying secret name.
const CredentialMountName = "cert-issuer"

// actuatorName is the registry name (§2.3 Actuator vocabulary).
const actuatorName = "cert-issuer"

// params is the Actuator's interpretation of Step params (Contract:
// contracts/actuators/cert-issuer.input.schema.json). The token is NOT here —
// it arrives as an injected credential file (§2.5).
type params struct {
	Operation  string `json:"operation"` // issue | renew | revoke
	Addr       string `json:"addr"`
	Mount      string `json:"mount"`
	Role       string `json:"role"`
	CommonName string `json:"commonName"`
	TTL        string `json:"ttl"`
	Serial     string `json:"serial"` // renew: the old serial to revoke; revoke: the target
}

// driverPy performs one PKI operation and prints one event line. It never
// echoes the token or the issued private key (§2.5, §1.8) — only serials and a
// sanitized status.
const driverPy = `import json, os, sys, urllib.request, urllib.error

base = "/runner/project"
cred = "/runner/credentials/cert-issuer"
with open(base + "/step.json") as f:
    p = json.load(f)
with open(cred + "/token") as f:
    token = f.read().strip()

addr = (p.get("addr") or "").rstrip("/")
mount = p.get("mount") or "pki"
op = p.get("operation")

def call(method, path, body=None):
    url = addr + "/v1/" + mount + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method,
        headers={"X-Vault-Token": token, "Content-Type": "application/json"})
    resp = urllib.request.urlopen(req, timeout=15)
    return json.load(resp)

ev = {"counter": 1, "host": p.get("commonName") or "cert-issuer", "ok": True, "detail": ""}
try:
    if op == "issue" or op == "renew":
        out = call("POST", "/issue/" + (p.get("role") or ""),
            {"common_name": p.get("commonName"), "ttl": p.get("ttl") or "720h"})
        ev["event"] = "cert_issued" if op == "issue" else "cert_renewed"
        ev["new_serial"] = out["data"]["serial_number"]
        # renew: revoke the superseded cert so the graph reflects the swap.
        if op == "renew" and p.get("serial"):
            call("POST", "/revoke", {"serial_number": p["serial"]})
            ev["old_serial"] = p["serial"]
    elif op == "revoke":
        out = call("POST", "/revoke", {"serial_number": p.get("serial")})
        ev["event"] = "cert_revoked"
        ev["serial"] = p.get("serial")
        ev["revocation_time"] = out["data"].get("revocation_time")
    else:
        ev.update(ok=False, event="cert_failed", detail="unknown operation")
except urllib.error.HTTPError as e:
    ev.update(ok=False, event="cert_failed", detail="http %d" % e.code)
except Exception as e:
    # NEVER str(e): CLM errors can embed the addr; class only (§2.5, §1.8).
    ev.update(ok=False, event="cert_failed", detail=type(e).__name__)

sys.stdout.write(json.dumps(ev) + "\n")
sys.stdout.flush()
sys.exit(0 if ev["ok"] else 1)
`

// Actuator implements the cert-issuer Actuator.
type Actuator struct{}

// Name implements actuators.Actuator.
func (Actuator) Name() string { return actuatorName }

// Prepare renders the driver + step config into pod content. The CLM token is
// projected separately by the dispatcher from the Step's CredentialRef (§2.5).
func (Actuator) Prepare(raw json.RawMessage, _ []actuators.Target) (actuators.JobSpec, error) {
	var p params
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return actuators.JobSpec{}, fmt.Errorf("cert-issuer: invalid params: %w", err)
		}
	}
	switch p.Operation {
	case "issue", "renew":
		if p.Role == "" || p.CommonName == "" {
			return actuators.JobSpec{}, fmt.Errorf("cert-issuer: %s requires role and commonName", p.Operation)
		}
	case "revoke":
		if p.Serial == "" {
			return actuators.JobSpec{}, fmt.Errorf("cert-issuer: revoke requires serial")
		}
	default:
		return actuators.JobSpec{}, fmt.Errorf("cert-issuer: unknown operation %q (issue, renew, revoke)", p.Operation)
	}
	if p.Addr == "" {
		return actuators.JobSpec{}, fmt.Errorf("cert-issuer: params.addr is required")
	}
	stepDoc, err := json.Marshal(p)
	if err != nil {
		return actuators.JobSpec{}, fmt.Errorf("cert-issuer: marshal step content: %w", err)
	}
	return actuators.JobSpec{
		Files: map[string]string{
			"project/driver.py": driverPy,
			"project/step.json": string(stepDoc),
		},
		Command: []string{"python3", "/runner/project/driver.py"},
	}, nil
}

// driverEvent is the one line the driver emits.
type driverEvent struct {
	Counter        int64  `json:"counter"`
	Event          string `json:"event"`
	Host           string `json:"host"`
	OK             bool   `json:"ok"`
	Detail         string `json:"detail"`
	NewSerial      string `json:"new_serial"`
	OldSerial      string `json:"old_serial"`
	Serial         string `json:"serial"`
	RevocationTime int64  `json:"revocation_time"`
}

// Interpret maps the driver's operation event to a task event + terminal
// result. Serials are non-secret and carried through for §1.8 traceability.
func (Actuator) Interpret(line []byte) (actuators.Interpreted, bool) {
	var ev driverEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Event == "" {
		return actuators.Interpreted{}, false
	}
	payload := map[string]any{}
	if ev.NewSerial != "" {
		payload["newSerial"] = ev.NewSerial
	}
	if ev.OldSerial != "" {
		payload["oldSerial"] = ev.OldSerial
	}
	if ev.Serial != "" {
		payload["serial"] = ev.Serial
	}
	if ev.Detail != "" {
		payload["detail"] = ev.Detail
	}
	out := actuators.Interpreted{Event: types.RunEvent{
		Seq:     ev.Counter,
		Kind:    ev.Event,
		Target:  ev.Host,
		Payload: payload,
	}}
	// Every driver line is terminal — one op, one event.
	status := actuators.StatusChanged
	if !ev.OK {
		status = actuators.StatusFailed
	}
	out.Result = &actuators.TargetResult{Target: ev.Host, Status: status, Failed: !ev.OK}
	return out, true
}
