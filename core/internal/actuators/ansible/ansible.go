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

	yaml "go.yaml.in/yaml/v3"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/types"
)

// Target is one Entity rendered as an execution target (the shared seam type).
type Target = actuators.Target

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

// params is this Actuator's interpretation of Step params — an internal
// convenience, not the Contract (§1.5): the pinned JSON-Schema Contract
// document lands with the Phase-2 Contract machinery.
type params struct {
	// Play is the play content (a YAML sequence of plays). Empty means the
	// Phase-0 gather-facts play.
	Play string `json:"play"`
	// EEImage overrides the dispatcher's default execution-environment
	// image. Dev accepts tags; production manifests pin digests (§7.3).
	EEImage string `json:"eeImage"`
}

// Actuator adapts this package's pure functions to the Actuator seam.
type Actuator struct{}

// Name implements actuators.Actuator.
func (Actuator) Name() string { return "ansible" }

// Prepare implements actuators.Actuator.
func (Actuator) Prepare(raw json.RawMessage, targets []Target) (actuators.JobSpec, error) {
	var p params
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return actuators.JobSpec{}, fmt.Errorf("ansible: invalid params: %w", err)
		}
	}
	play := p.Play
	if play == "" {
		play = GatherFactsPlay
	} else if err := validatePlay(play); err != nil {
		// Fail at Prepare (terminal), not as a cryptic pod crash.
		return actuators.JobSpec{}, err
	}
	content := BuildContent(play, targets)
	return actuators.JobSpec{
		Files: map[string]string{
			"project/play.yml": content.Play,
			"inventory/hosts":  content.Hosts,
		},
		// -j: one JSON event per stdout line — the event stream the
		// dispatcher ships to NATS.
		Command: []string{"ansible-runner", "run", "/runner", "-p", "play.yml", "-j"},
		Image:   p.EEImage,
	}, nil
}

// validatePlay checks the content is a YAML sequence (a plays list) — the
// only shape ansible-runner will accept as a play file.
func validatePlay(play string) error {
	var doc any
	if err := yaml.Unmarshal([]byte(play), &doc); err != nil {
		return fmt.Errorf("ansible: params.play is not valid YAML: %w", err)
	}
	if _, ok := doc.([]any); !ok {
		return fmt.Errorf("ansible: params.play must be a YAML sequence of plays")
	}
	return nil
}

// Interpret implements actuators.Actuator.
func (Actuator) Interpret(line []byte) (actuators.Interpreted, bool) {
	ev, ok := ParseEvent(line)
	if !ok {
		return actuators.Interpreted{}, false
	}
	out := actuators.Interpreted{Event: ToRunEvent("", ev)}
	if host, status := HostStatus(ev); host != "" && status != "" {
		out.Result = &actuators.TargetResult{
			Target: host,
			Status: status,
			Failed: status == actuators.StatusFailed || status == actuators.StatusUnreachable,
		}
	}
	out.Facts = ExtractFacts(ev)
	return out, true
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

// HostStatus classifies terminal per-target events into the seam's statuses
// (empty status = not a terminal per-target event). runner_on_ok with a
// changed result reports "changed" — ok, but the target was mutated.
func HostStatus(ev RunnerEvent) (host, status string) {
	h, _ := ev.EventData["host"].(string)
	switch ev.Event {
	case "runner_on_ok":
		if res, ok := ev.EventData["res"].(map[string]any); ok {
			if changed, _ := res["changed"].(bool); changed {
				return h, actuators.StatusChanged
			}
		}
		return h, actuators.StatusOK
	case "runner_on_skipped":
		// A host whose task was skipped completed it without failure: ok
		// for the per-target fold (failures stay sticky over it).
		return h, actuators.StatusOK
	case "runner_on_failed":
		return h, actuators.StatusFailed
	case "runner_on_unreachable":
		return h, actuators.StatusUnreachable
	}
	return h, ""
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
