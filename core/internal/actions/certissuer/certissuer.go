// Package certissuer ships the write-side Actions of the cert-issuer Connector
// (charter §2.2, ADR-0031): issue / renew / revoke against a Vault-compatible
// PKI CLM (dev: OpenBao). These are the charter's `revoke-cert`-shaped typed
// operations — each a single contracted call with an input + output Contract and
// idempotency/dry-run declarations, no longer an Actuator in disguise (retiring
// the ADR-0030 guardian flag).
//
// Like every credentialed operation they run inside an execution pod so the CLM
// token injects as a CredentialRef file at spawn (§2.5); the driver reads it
// from /runner/credentials/cert-issuer/token and emits one terminal event with
// typed outputs — never the token or the issued private key (§2.5, §1.8).
package certissuer

import (
	"encoding/json"
	"fmt"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/types"
)

// CredentialMountName is the fixed RefName a cert Action's CredentialRef must
// use, so the driver reads /runner/credentials/cert-issuer/token regardless of
// the underlying secret name.
const CredentialMountName = "cert-issuer"

// Action implements one cert operation. Three constructors (Issue/Renew/Revoke)
// configure the operation + its idempotency declaration; all share the driver,
// Interpret, and dry-run support.
type Action struct {
	op         string // issue | renew | revoke
	idempotent bool
}

// Issue mints a new leaf certificate. Not idempotent — each call is a new cert.
func Issue() Action { return Action{op: "issue"} }

// Renew issues a replacement cert and revokes the superseded serial. Not
// idempotent — each call mints a new cert.
func Renew() Action { return Action{op: "renew"} }

// Revoke revokes a certificate by serial. Idempotent — revoking an
// already-revoked cert is a no-op at the CLM.
func Revoke() Action { return Action{op: "revoke", idempotent: true} }

// Name implements actions.Action (namespaced by Connector).
func (a Action) Name() string { return "certissuer/" + a.op }

// Idempotent implements actions.Action.
func (a Action) Idempotent() bool { return a.idempotent }

// DryRunnable implements actions.Action — every cert op supports a plan.
func (a Action) DryRunnable() bool { return true }

// params is the Action's interpretation of input params (Contract:
// actions/certissuer/<op>.input). The token is NOT here — injected file (§2.5).
type params struct {
	Addr       string `json:"addr"`
	Mount      string `json:"mount"`
	Role       string `json:"role"`
	CommonName string `json:"commonName"`
	TTL        string `json:"ttl"`
	Serial     string `json:"serial"`
}

// Prepare renders the driver + step config into pod content. dryRun asks the
// driver to plan (no CLM write). The credential is projected separately (§2.5).
func (a Action) Prepare(raw json.RawMessage, dryRun bool) (actuators.JobSpec, error) {
	var p params
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return actuators.JobSpec{}, fmt.Errorf("certissuer/%s: invalid params: %w", a.op, err)
		}
	}
	if p.Addr == "" {
		return actuators.JobSpec{}, fmt.Errorf("certissuer/%s: params.addr is required", a.op)
	}
	switch a.op {
	case "issue", "renew":
		if p.Role == "" || p.CommonName == "" {
			return actuators.JobSpec{}, fmt.Errorf("certissuer/%s requires role and commonName", a.op)
		}
	case "revoke":
		if p.Serial == "" {
			return actuators.JobSpec{}, fmt.Errorf("certissuer/revoke requires serial")
		}
	}
	step, err := json.Marshal(map[string]any{
		"operation": a.op, "dryRun": dryRun,
		"addr": p.Addr, "mount": p.Mount, "role": p.Role,
		"commonName": p.CommonName, "ttl": p.TTL, "serial": p.Serial,
	})
	if err != nil {
		return actuators.JobSpec{}, fmt.Errorf("certissuer/%s: marshal step: %w", a.op, err)
	}
	return actuators.JobSpec{
		Files: map[string]string{
			"project/driver.py": driverPy,
			"project/step.json": string(step),
		},
		Command: []string{"python3", "/runner/project/driver.py"},
	}, nil
}

// driverEvent is the one line the driver emits.
type driverEvent struct {
	Counter int64           `json:"counter"`
	Event   string          `json:"event"`
	Host    string          `json:"host"`
	OK      bool            `json:"ok"`
	Detail  string          `json:"detail"`
	Outputs json.RawMessage `json:"outputs"`
}

// Interpret maps the driver's terminal event to a task event, result, and the
// Action's typed Outputs (validated against its output Contract downstream).
func (a Action) Interpret(line []byte) (actuators.Interpreted, bool) {
	var ev driverEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Event == "" {
		return actuators.Interpreted{}, false
	}
	payload := map[string]any{}
	if ev.Detail != "" {
		payload["detail"] = ev.Detail
	}
	if len(ev.Outputs) > 0 {
		payload["outputs"] = ev.Outputs
	}
	out := actuators.Interpreted{
		Event: types.RunEvent{
			Seq:     ev.Counter,
			Kind:    ev.Event,
			Target:  ev.Host,
			Payload: payload,
		},
		Outputs: ev.Outputs,
	}
	status := actuators.StatusChanged
	if !ev.OK {
		status = actuators.StatusFailed
	}
	out.Result = &actuators.TargetResult{Target: ev.Host, Status: status, Failed: !ev.OK}
	return out, true
}
