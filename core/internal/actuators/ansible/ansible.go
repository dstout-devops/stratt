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
	// Check runs the play in check mode with diff capture (--check --diff):
	// nothing mutates, and "changed" means "would change" — the Baseline
	// drift signal (ADR-0019).
	Check bool `json:"check"`
	// SCM is an SCM content-ref: clone a repo in the EE pod and run a
	// playbook from it (charter §5.6). Mutually exclusive with Play.
	SCM *scmParams `json:"scm"`
	// ExtraVars are variables passed to ansible-runner as --extra-vars (via
	// env/extravars) — the landing field for AWX launch extra_vars and survey
	// answers (ADR-0025/0026). Never secret material (§2.5).
	ExtraVars map[string]any `json:"extraVars"`
}

// scmParams is the SCM content-ref (ansible.input v3). The clone runs inside
// the EE image (git is exec'd there, never linked into the control plane —
// §3 GPL boundary); the control plane only emits the clone script + env.
type scmParams struct {
	Repo     string `json:"repo"`
	Ref      string `json:"ref"`
	Playbook string `json:"playbook"`
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
	if p.Play != "" && p.SCM != nil {
		return actuators.JobSpec{}, fmt.Errorf("ansible: params.play and params.scm are mutually exclusive")
	}
	hosts := BuildContent("", targets).Hosts // the View's targets, always

	if p.SCM != nil {
		return prepareSCM(p, hosts)
	}

	play := p.Play
	if play == "" {
		play = GatherFactsPlay
	} else if err := validatePlay(play); err != nil {
		// Fail at Prepare (terminal), not as a cryptic pod crash.
		return actuators.JobSpec{}, err
	}
	// -j: one JSON event per stdout line — the event stream the
	// dispatcher ships to NATS.
	command := []string{"ansible-runner", "run", "/runner", "-p", "play.yml", "-j"}
	if p.Check {
		// Check mode with diffs: read-only by the tool's own contract;
		// changed results mean "would change" (ADR-0019).
		command = append(command, "--cmdline", "--check --diff")
	}
	files := map[string]string{
		"project/play.yml": play,
		"inventory/hosts":  hosts,
	}
	if err := addExtraVars(files, p.ExtraVars); err != nil {
		return actuators.JobSpec{}, err
	}
	return actuators.JobSpec{
		Files:   files,
		Command: command,
		Image:   p.EEImage,
	}, nil
}

// addExtraVars writes ansible-runner's env/extravars file when extra vars are
// present. ansible-runner loads /runner/env/extravars as --extra-vars; JSON is
// valid YAML, so the marshalled map is accepted verbatim.
func addExtraVars(files map[string]string, vars map[string]any) error {
	if len(vars) == 0 {
		return nil
	}
	raw, err := json.Marshal(vars)
	if err != nil {
		return fmt.Errorf("ansible: invalid extraVars: %w", err)
	}
	files["env/extravars"] = string(raw)
	return nil
}

// cloneScript clones the SCM content-ref into /runner/project (an empty,
// writable dir in the EE image) then runs the playbook from it. Untrusted
// values (repo/ref/playbook) arrive as env and are used quoted — never
// interpolated into the script text. A private-repo credential, when present,
// is injected as a file by the dispatcher (§2.5) and referenced here via
// GIT_SSH_COMMAND set out-of-band; this v1 targets public/pre-seeded repos.
const cloneScript = `set -eu
git clone --depth 1 ${SCM_REF:+--branch} ${SCM_REF:+"$SCM_REF"} -- "$SCM_REPO" /runner/project
if [ "${SCM_CHECK:-}" = "1" ]; then
  exec ansible-runner run /runner -p "$SCM_PLAYBOOK" -j --cmdline "--check --diff"
fi
exec ansible-runner run /runner -p "$SCM_PLAYBOOK" -j
`

// prepareSCM builds the JobSpec for an SCM content-ref Step. The playbook body
// is cloned in-pod; only inventory/hosts (from the View) and the static clone
// script are mounted.
func prepareSCM(p params, hosts string) (actuators.JobSpec, error) {
	if err := validateSCM(p.SCM); err != nil {
		return actuators.JobSpec{}, err
	}
	env := map[string]string{
		"SCM_REPO":     p.SCM.Repo,
		"SCM_PLAYBOOK": p.SCM.Playbook,
	}
	if p.SCM.Ref != "" {
		env["SCM_REF"] = p.SCM.Ref
	}
	if p.Check {
		env["SCM_CHECK"] = "1"
	}
	files := map[string]string{
		"clone.sh":        cloneScript,
		"inventory/hosts": hosts,
	}
	if err := addExtraVars(files, p.ExtraVars); err != nil {
		return actuators.JobSpec{}, err
	}
	return actuators.JobSpec{
		Files:   files,
		Command: []string{"sh", "/runner/clone.sh"},
		Env:     env,
		Image:   p.EEImage,
	}, nil
}

// validateSCM rejects an SCM content-ref that would fail in-pod: empty
// repo/playbook, or a playbook path that escapes the clone via traversal.
func validateSCM(s *scmParams) error {
	if s.Repo == "" || s.Playbook == "" {
		return fmt.Errorf("ansible: params.scm requires repo and playbook")
	}
	// A repo (or ref) beginning with "-" is parsed by git as an option, not a
	// URL — argument injection that survives shell-quoting. The clone script
	// also guards this with a "--" separator (defense in depth).
	if strings.HasPrefix(s.Repo, "-") {
		return fmt.Errorf("ansible: params.scm.repo must not begin with '-'")
	}
	if strings.HasPrefix(s.Ref, "-") {
		return fmt.Errorf("ansible: params.scm.ref must not begin with '-'")
	}
	if strings.Contains(s.Playbook, "..") || strings.HasPrefix(s.Playbook, "/") {
		return fmt.Errorf("ansible: params.scm.playbook must be a relative path within the repo")
	}
	return nil
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
	out.Drift = ExtractDiff(ev)
	return out, true
}

// ExtractDiff lifts a changed task's observed-vs-expected detail from a
// runner_on_ok event into a drift fragment (ADR-0019): the task name plus
// the STRUCTURE of the tool's diff — which files/objects would change —
// never the before/after bodies. Diff bodies are rendered file content and
// can carry secret material; the persisted Finding must not (§2.5,
// charter-guardian on ADR-0019). Full detail stays on the Run's event
// stream, mirroring the opentofu posture (addresses/actions in fragments;
// values only in the redacted plan-json event). Nil for unchanged tasks.
func ExtractDiff(ev RunnerEvent) json.RawMessage {
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

// diffPaths reduces an ansible --diff document to the changed objects'
// headers (file paths / object names) — structure only, no content.
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

	// os.hardening.* — projected by the os-hardening collector gather play,
	// which set_facts a `stratt_hardening_<domain>` dict per CIS domain (§8:
	// facts return as Facets). The play owns the normalization (sysctl/sshd
	// values as strings, filesystem/auditd/services as booleans) so the
	// projected document matches the pinned os.hardening.<domain> schema —
	// a mismatch is refused at the Projector write (charter §1.1). Each
	// domain is demanded by the shipping cis facet-observation Baselines.
	for _, domain := range hardeningDomains {
		m, ok := facts["stratt_hardening_"+domain].(map[string]any)
		if !ok || len(m) == 0 {
			continue
		}
		if raw, err := json.Marshal(m); err == nil {
			out["os.hardening."+domain] = raw
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// hardeningDomains are the os.hardening.<domain> Facet namespaces the CIS
// collector projects; each has a pinned schema in contracts/facets and is
// demanded by a shipping Baseline (charter §1.1).
var hardeningDomains = []string{"sysctl", "sshd", "filesystem", "auditd", "services"}
