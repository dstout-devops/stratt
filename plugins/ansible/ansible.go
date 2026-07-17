// Package ansible is the Ansible content-expertise, extracted from the in-tree
// core/internal/actuators/ansible into the EE-image shim (ADR-0051). Pure functions:
// render inventory from the core-resolved targets, and map `ansible-runner`'s -json
// events onto the sovereign port's typed shapes. No graph write path, no ansible
// dependency — the plugin proposes typed values; the hub governs.
package ansible

import (
	"encoding/json"
	"fmt"
	"strings"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Target is one core-resolved actuation target passed LEGIBLY to the shim (ADR-0051
// MF4): the shim renders its inventory FROM these, never from the playbook's
// self-reported hosts, so the hub's confused-deputy gate binds. Identity keys carry
// the write-back correlation (a host's facts project onto its Entity by identity).
type Target struct {
	Name     string            `json:"name"`
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
// target Name with its connection Vars (ADR-0051 MF4: from the core-resolved set).
func buildInventory(targets []Target) string {
	var b strings.Builder
	b.WriteString("[all]\n")
	for _, t := range targets {
		b.WriteString(t.Name)
		for k, v := range t.Vars {
			fmt.Fprintf(&b, " %s=%s", k, v)
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
func parseEvent(line []byte) (RunnerEvent, bool) {
	var ev RunnerEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Event == "" {
		return RunnerEvent{}, false
	}
	return ev, true
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
