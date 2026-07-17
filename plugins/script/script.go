// Package script is the script Actuator content-expertise, extracted from the
// in-tree core/internal/actuators/script into the EE-image shim (ADR-0046/0051). It
// runs a user-supplied script once per core-resolved target and maps each target's
// exit onto the sovereign port's typed shapes. No graph write path, no core
// dependency — the plugin proposes typed values; the hub governs.
package script

import (
	"encoding/json"
	"strings"
)

// Target is one core-resolved actuation target passed LEGIBLY to the shim (ADR-0051
// MF4): the shim runs the script against exactly these (never a self-reported set),
// so the hub's confused-deputy gate binds. Vars are exposed to the script as
// STRATT_VAR_<KEY>; the internal graph id deliberately does NOT cross (a user script
// keys on identity/vars, not the core's rebuildable-projection id — §1.2).
type Target struct {
	Name string            `json:"name"`
	Vars map[string]string `json:"vars,omitempty"`
}

// Request is what the shim reads from the Job content (the opaque params + the
// legible targets). DryRun is unused — script is effectful and declares no read-only
// capability (the hub rejects a dry-run of it at launch, ADR-0046 Category B).
type Request struct {
	Params  json.RawMessage `json:"params"`
	Targets []Target        `json:"targets"`
}

// params is the shim's read of the opaque desired — never the Contract (§1.5).
type params struct {
	Script      string `json:"script"`
	Interpreter string `json:"interpreter"`
}

// interpreterFor validates the requested interpreter against the two runtimes the EE
// image guarantees, defaulting to sh.
func interpreterFor(p params) (string, bool) {
	switch p.Interpreter {
	case "", "sh":
		return "sh", true
	case "python3":
		return "python3", true
	default:
		return "", false
	}
}

// scriptEnv renders a target's environment for the user script: its name plus each
// connection var as STRATT_VAR_<KEY> (upper-cased, . and - → _), matching the legacy
// in-tree driver's vocabulary so existing scripts keep working.
func scriptEnv(t Target) []string {
	env := []string{"STRATT_TARGET_NAME=" + t.Name}
	for k, v := range t.Vars {
		env = append(env, "STRATT_VAR_"+envKey(k)+"="+v)
	}
	return env
}

func envKey(k string) string {
	up := strings.ToUpper(k)
	up = strings.ReplaceAll(up, ".", "_")
	return strings.ReplaceAll(up, "-", "_")
}
