// Package ansible is the ansible Actuator (charter §2.3): it prepares
// tool content for ansible-runner and interprets the runner's event stream.
//
// GPL boundary (§3, ADR-0002): this package never imports Ansible. It writes
// tool-content files and parses JSON events; ansible-runner executes as a
// subprocess in a separate EE image inside a K8s Job pod.
//
// Inside tool content, tool-native words (playbook, inventory) are the tool's
// own rendering and are fine (§2); they never name Stratt core-model
// identifiers.
package ansible

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// Target is one Entity rendered as an execution target.
type Target struct {
	EntityID string
	// Name is the host alias used in tool content and per-target results.
	Name string
	// Vars are tool-level connection/host vars (e.g. ansible_connection).
	Vars map[string]string
}

// Content is the material mounted into the EE pod's private data dir.
type Content struct {
	// Play is the play file (project/play.yml).
	Play string
	// Hosts is the runner hosts file, generated from the View's Entities —
	// the View is the Stratt-side truth; this file is a rendering (§2).
	Hosts string
}

// GatherFactsPlay is the Phase-0 spike play: gather facts from every target
// so they project back as Facets (§8). Runs with local connection semantics
// against simulated estates (vcsim VMs are not reachable machines); the same
// content shape carries SSH connections against real ones.
const GatherFactsPlay = `- hosts: all
  gather_facts: true
  tasks:
    - name: report
      ansible.builtin.debug:
        msg: "stratt phase-0 fact gathering complete"
`

// BuildContent renders targets into runner content.
func BuildContent(play string, targets []Target) Content {
	var b strings.Builder
	b.WriteString("[all]\n")
	for _, t := range targets {
		b.WriteString(t.Name)
		for k, v := range t.Vars {
			fmt.Fprintf(&b, " %s=%s", k, v)
		}
		b.WriteByte('\n')
	}
	return Content{Play: play, Hosts: b.String()}
}

// RunnerEvent is the subset of an ansible-runner JSON event the platform
// interprets. Everything else passes through opaquely in the payload.
type RunnerEvent struct {
	UUID      string         `json:"uuid"`
	Counter   int64          `json:"counter"`
	Event     string         `json:"event"`
	Stdout    string         `json:"stdout"`
	EventData map[string]any `json:"event_data"`
}

// ParseEvent decodes one stdout line from `ansible-runner run -j`. Lines that
// are not JSON events (runner banner noise) return ok=false.
func ParseEvent(line []byte) (RunnerEvent, bool) {
	var ev RunnerEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Event == "" {
		return RunnerEvent{}, false
	}
	return ev, true
}

// ToRunEvent maps a runner event onto the platform's task-event shape — the
// floor of the §1.8 descent ladder.
func ToRunEvent(runID string, ev RunnerEvent) types.RunEvent {
	target := ""
	if h, ok := ev.EventData["host"].(string); ok {
		target = h
	}
	return types.RunEvent{
		RunID:  runID,
		Seq:    ev.Counter,
		Kind:   ev.Event,
		Target: target,
		Payload: map[string]any{
			"uuid":       ev.UUID,
			"stdout":     ev.Stdout,
			"event_data": ev.EventData,
		},
	}
}

// HostResult classifies terminal per-target events.
func HostResult(ev RunnerEvent) (host string, ok, failed bool) {
	h, _ := ev.EventData["host"].(string)
	switch ev.Event {
	case "runner_on_ok":
		return h, true, false
	case "runner_on_failed", "runner_on_unreachable":
		return h, false, true
	}
	return h, false, false
}

// ExtractFacts pulls gathered ansible_facts from a runner_on_ok event, mapped
// to the Facet namespaces the platform projects (charter §8: facts return as
// Facets). Returns nil when the event carries no facts.
func ExtractFacts(ev RunnerEvent) map[string]json.RawMessage {
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

	out := map[string]json.RawMessage{}

	// os.kernel — the charter's own example namespace (§2.1).
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
		raw, err := json.Marshal(kernel)
		if err == nil {
			out["os.kernel"] = raw
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
