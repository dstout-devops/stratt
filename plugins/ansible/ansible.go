// Package ansible is the Ansible content-expertise, extracted from the in-tree
// core/internal/actuators/ansible into the EE-image shim (ADR-0051). Pure functions:
// render inventory from the core-resolved targets, and map `ansible-runner`'s -json
// events onto the sovereign port's typed shapes. No graph write path, no ansible
// dependency — the plugin proposes typed values; the hub governs.
package ansible

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Target is one core-resolved actuation target passed LEGIBLY to the shim (ADR-0051
// MF4): the shim renders its inventory FROM these, never from the playbook's
// self-reported hosts, so the hub's confused-deputy gate binds. Identity keys carry
// the write-back correlation (a host's facts project onto its Entity by identity).
type Target struct {
	Name string `json:"name"`
	// Address is the core's typed reachability coordinate (ADR-0084). The shim —
	// not the core — renders it into ansible's connection var: the spine never
	// authors ansible_host.
	Address string `json:"address,omitempty"`
	// Port is the core's optional typed management port paired with Address
	// (ADR-0084). Same rule as Address: the SHIM turns it into ansible_port; the
	// spine never authors an ansible key. 0 ⇒ undeclared, so ansible's own default
	// applies — the shim never substitutes 22.
	Port     int32             `json:"port,omitempty"`
	Vars     map[string]string `json:"vars,omitempty"`
	Identity map[string]string `json:"identity,omitempty"`
}

// Request is what the shim reads from the Job content (params + the legible targets +
// the dry-run bit). Params is the opaque desired the core never interprets (§1.1).
type Request struct {
	Params  json.RawMessage `json:"params"`
	Targets []Target        `json:"targets"`
	DryRun  bool            `json:"dryRun"`
}

// GatherFactsPlay is the default play: gather facts from every target so they project
// back as Facets (charter §8). Local-connection semantics against simulated estates;
// the same shape carries SSH connections against real ones.
const GatherFactsPlay = `- hosts: all
  gather_facts: true
  tasks:
    - name: report
      ansible.builtin.debug:
        msg: "stratt fact gathering complete"
`

// buildInventory renders the legible targets into an INI [all] group — one line per
// target Name (ADR-0051 MF4: from the core-resolved set). The SHIM maps the core's
// typed Address (ADR-0084) into ansible's connection var — the core never authored
// an ansible_host: a real address becomes `ansible_host=<addr>`, joined by
// `ansible_port=<port>` when the Facet declared one; the reserved value `local`
// becomes `ansible_connection=local` (a target that explicitly declares it runs on
// the control node — a control-node connection has no network port, so Port is not
// rendered there); an empty Address emits no connection var, so an unreachable host
// fails LOUDLY rather than silently running local (§1.8). Genuine tool Vars (never a
// connection key from core) still render, in sorted key order so the same resolved
// target set always renders the SAME inventory — a byte-stable artifact is what makes
// two Runs comparable during descent (§1.8).
func buildInventory(targets []Target) string {
	var b strings.Builder
	b.WriteString("[all]\n")
	for _, t := range targets {
		b.WriteString(t.Name)
		switch {
		case t.Address == "local":
			b.WriteString(" ansible_connection=local")
		case t.Address != "":
			fmt.Fprintf(&b, " ansible_host=%s", t.Address)
			if t.Port > 0 {
				fmt.Fprintf(&b, " ansible_port=%d", t.Port)
			}
		}
		for _, k := range slices.Sorted(maps.Keys(t.Vars)) {
			fmt.Fprintf(&b, " %s=%s", k, t.Vars[k])
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// RunnerEvent is the subset of an ansible-runner JSON event the shim interprets.
type RunnerEvent struct {
	UUID      string         `json:"uuid"`
	Counter   int64          `json:"counter"`
	Event     string         `json:"event"`
	Stdout    string         `json:"stdout"`
	EventData map[string]any `json:"event_data"`
}

// parseEvent decodes one `ansible-runner run -j` stdout line; non-event lines
// (runner banners, tracebacks) return ok=false — the caller surfaces them as
// diagnostics (ADR-0051 MF5), never dropped.
//
// UseNumber is LOAD-BEARING, not a style preference. EventData is map[string]any, and
// encoding/json decodes every JSON number in an `any` into float64 — but real module
// results carry integers far outside float64's range: an RSA modulus from
// community.crypto.openssl_privatekey is ~617 digits. encoding/json then rejects the
// WHOLE line ("cannot unmarshal number … into Go value of type float64"), so a genuine
// runner_on_ok / runner_on_failed silently degraded into an untyped diagnostic and its
// ItemResult, facts write-back, and drift were LOST (§1.8 — the abstraction must never
// hide diagnosis). json.Number keeps the literal instead, so nothing overflows and
// facts round-trip byte-exactly rather than through float64. Live-verified against the
// EE image: a one-task openssl_privatekey play emitted ZERO ItemResults before this.
func parseEvent(line []byte) (RunnerEvent, bool) {
	var ev RunnerEvent
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	if err := dec.Decode(&ev); err != nil || ev.Event == "" {
		return RunnerEvent{}, false
	}
	return ev, true
}

// eventShaped reports that line IS a JSON object carrying a non-empty "event" field —
// an ansible-runner event parseEvent should have understood. It separates a real decode
// failure (a type-shape surprise that LOSES typed signal) from the ordinary non-JSON
// banners and tracebacks MF5 legitimately forwards as diagnostics. Without this
// distinction a misparse is indistinguishable from normal output, which is exactly how
// the float64 overflow above survived unnoticed.
func eventShaped(line []byte) bool {
	var probe struct {
		Event string `json:"event"`
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber() // never let the probe itself overflow — it must classify, not parse
	return dec.Decode(&probe) == nil && probe.Event != ""
}

// hostStatus classifies a terminal per-host event into the port's ItemResult status
// (STATUS_UNSPECIFIED = not a terminal per-host event). runner_on_ok+changed is
// CHANGED (ok, but mutated).
func hostStatus(ev RunnerEvent) (host string, status pluginv1.ItemResult_Status) {
	h, _ := ev.EventData["host"].(string)
	switch ev.Event {
	case "runner_on_ok":
		if res, ok := ev.EventData["res"].(map[string]any); ok {
			if changed, _ := res["changed"].(bool); changed {
				return h, pluginv1.ItemResult_STATUS_CHANGED
			}
		}
		return h, pluginv1.ItemResult_STATUS_OK
	case "runner_on_skipped":
		return h, pluginv1.ItemResult_STATUS_OK
	case "runner_on_failed":
		return h, pluginv1.ItemResult_STATUS_FAILED
	case "runner_on_unreachable":
		return h, pluginv1.ItemResult_STATUS_UNREACHABLE
	}
	return h, pluginv1.ItemResult_STATUS_UNSPECIFIED
}

// hardeningDomains are the os.hardening.<domain> Facet namespaces the CIS collector
// projects (each pinned + Baseline-demanded, charter §1.1).
var hardeningDomains = []string{"sysctl", "sshd", "filesystem", "auditd", "services"}

// extractFacts pulls gathered ansible_facts from a runner_on_ok event, mapped to the
// Facet namespaces the platform projects (§8). Returns nil when the event has none.
func extractFacts(ev RunnerEvent) map[string][]byte {
	res, ok := ev.EventData["res"].(map[string]any)
	if !ok {
		return nil
	}
	facts, ok := res["ansible_facts"].(map[string]any)
	if !ok || len(facts) == 0 {
		return nil
	}
	get := func(key string) (string, bool) {
		v, ok := facts[key].(string)
		return v, ok && v != ""
	}
	out := map[string][]byte{}

	kernel := map[string]string{}
	if v, ok := get("ansible_system"); ok {
		kernel["family"] = strings.ToLower(v)
	}
	if v, ok := get("ansible_kernel"); ok {
		kernel["release"] = v
	}
	if v, ok := get("ansible_architecture"); ok {
		kernel["arch"] = v
	}
	if len(kernel) > 0 {
		if raw, err := json.Marshal(kernel); err == nil {
			out["os.kernel"] = raw
		}
	}
	for _, domain := range hardeningDomains {
		if m, ok := facts["stratt_hardening_"+domain].(map[string]any); ok && len(m) > 0 {
			if raw, err := json.Marshal(m); err == nil {
				out["os.hardening."+domain] = raw
			}
		}
	}
	if m, ok := facts["stratt_fileset"].(map[string]any); ok && len(m) > 0 {
		if raw, err := json.Marshal(m); err == nil {
			out["fileset.content"] = raw
		}
	}
	if arr, ok := facts["stratt_access"].([]any); ok && len(arr) > 0 {
		if raw, err := json.Marshal(arr); err == nil {
			out["access.grants"] = raw
		}
	}
	// Generic facet-report convention (ADR-0084 fact-back): a play projects ANY Facet
	// by setting the reserved `stratt_facets` fact — a map of namespace -> value (the
	// AWX artifacts model: the remediation reports the state it OBSERVED on the target).
	// The per-Run FacetWriteScope (ADR-0054) gates WHICH namespaces are accepted
	// core-side, so this convention never widens a Run's write authority. Generalizes
	// the fixed stratt_fileset/stratt_hardening_* keys above without a per-facet map.
	if sf, ok := facts["stratt_facets"].(map[string]any); ok {
		for ns, v := range sf {
			if raw, err := json.Marshal(v); err == nil {
				out[ns] = raw
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// extractDiff lifts a changed task's drift STRUCTURE (task + changed file/object
// headers) from a runner_on_ok event — never the before/after bodies, which carry
// secret material (§2.5, ADR-0019). Nil for unchanged tasks.
func extractDiff(ev RunnerEvent) []byte {
	if ev.Event != "runner_on_ok" {
		return nil
	}
	res, ok := ev.EventData["res"].(map[string]any)
	if !ok {
		return nil
	}
	if changed, _ := res["changed"].(bool); !changed {
		return nil
	}
	fragment := map[string]any{"changed": true}
	if task, _ := ev.EventData["task"].(string); task != "" {
		fragment["task"] = task
	}
	if paths := diffPaths(res["diff"]); len(paths) > 0 {
		fragment["paths"] = paths
	}
	raw, err := json.Marshal(fragment)
	if err != nil {
		return nil
	}
	return raw
}

func diffPaths(diff any) []string {
	entries, ok := diff.([]any)
	if !ok {
		return nil
	}
	var paths []string
	for _, d := range entries {
		m, ok := d.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"after_header", "before_header"} {
			if s, _ := m[key].(string); s != "" {
				paths = append(paths, s)
				break
			}
		}
	}
	return paths
}
