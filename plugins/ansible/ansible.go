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
	"io"
	"maps"
	"slices"
	"sort"
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
	// Jump is the core-resolved reached-via chain, NEAREST HOP FIRST (ADR-0126 D3).
	// Coordinates only — authenticating to a hop is params.connection.jump. The SHIM
	// turns these into -o ProxyJump=…; the spine never authors an ssh flag (§1.4).
	Jump []Hop `json:"jump,omitempty"`
	// Transport is the target's OBSERVED connection method (ADR-0156): kind plus the
	// coordinates document, opaque to core and read here. Empty kind ⇒ nothing observed and
	// ansible's own default applies — DISTINCT from an observed `ssh`, which means a Syncer
	// determined it. The SHIM renders every ansible_* key from this; the spine authors none.
	TransportKind        string          `json:"transportKind,omitempty"`
	TransportCoordinates json.RawMessage `json:"transportCoordinates,omitempty"`
	// Groups are the named partitions the CORE resolved this target into (ADR-0161 D1). The SHIM
	// renders them as INI sections; the core authors no INI and knows nothing about ansible groups.
	// Empty ⇒ the target appears in [all] only, which is every Run before ADR-0161.
	Groups []string `json:"groups,omitempty"`
}

// Hop is one bastion's coordinate in a reached-via chain.
type Hop struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
	Port    int32  `json:"port,omitempty"`
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
// hasLocalTarget reports whether any target renders ansible_connection=local as a HOST
// var — the fact connectionTypeVars needs to refuse a contradicting group-level type
// (ADR-0153 D6). Kept beside buildInventory because it must stay true to what that
// function actually emits; two functions disagreeing about which targets are local is
// how the refusal would silently stop firing.
func hasLocalTarget(targets []Target) bool {
	for _, t := range targets {
		if t.Address == "local" {
			return true
		}
	}
	return false
}

// buildInventory renders the target set as an INI inventory: `[all]` with one fully-specified host
// line each, then one section per named group (ADR-0161 D3).
//
// THE HOST DEFINITIONS LIVE IN [all], AND THE GROUPS LIST BARE NAMES. That is ansible's own idiom
// and it is load-bearing here: a host repeated with its vars in several sections would define the
// same host several times, and ansible's precedence between those definitions is a rule nobody
// should have to know. One definition, referenced by name.
//
// GROUPS NEVER WIDEN THE RUN (D3). Every name in a group section is a host already in `[all]`,
// because membership was resolved from the very targets the View selected. A group is a partition
// of the blast radius, never a lookup back into the graph — if it could pull in an unselected host,
// the play's `hosts:` line would become the authorization unit, which is content deciding blast
// radius and exactly what ADR-0028 refuses.
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
		// The OBSERVED transport, as HOST vars (ADR-0156 D3) — which is what lets one Run
		// converge a pod, a vSphere VM and an EC2 instance together. Errors are surfaced by
		// renderInventory's caller; buildInventory keeps its byte-stable signature, and a
		// target whose transport does not render simply carries none here.
		tv, _ := transportVarsFor(t)
		for _, k := range slices.Sorted(maps.Keys(tv)) {
			fmt.Fprintf(&b, " %s=%s", k, tv[k])
		}
		for _, k := range slices.Sorted(maps.Keys(t.Vars)) {
			fmt.Fprintf(&b, " %s=%s", k, t.Vars[k])
		}
		b.WriteByte('\n')
	}
	// One section per group, sorted, members sorted — the byte-stability §1.8 needs so two Runs over
	// one target set are comparable during descent. Map iteration would break it on its own.
	members := map[string][]string{}
	for _, t := range targets {
		for _, g := range t.Groups {
			members[g] = append(members[g], t.Name)
		}
	}
	for _, g := range slices.Sorted(maps.Keys(members)) {
		hosts := members[g]
		sort.Strings(hosts)
		b.WriteString("\n[" + g + "]\n")
		for _, h := range hosts {
			b.WriteString(h + "\n")
		}
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
	// Exhaustion check — NOT redundant. json.Unmarshal rejects trailing content; a
	// json.Decoder stops at the end of the first value and ignores the rest. Moving to a
	// Decoder for UseNumber therefore silently loosened this seam: a TORN or CONCATENATED
	// line would parse as its first event and everything after it would vanish — no
	// ItemResult, no diagnostic, no unparsed-event WARN. That is precisely the
	// invisibility the UseNumber fix exists to remove, so the strictness is restored
	// here. Trailing whitespace is fine (io.EOF); a second value or garbage is not, and
	// such a line is still eventShaped, so it lands on the WARN channel as a defect.
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return RunnerEvent{}, false
	}
	return ev, true
}

// eeContentManifestPath is where the EE build records the content it actually installed
// (ee/content.py, ADR-0117 D3). It is a file contract WITHIN the image — the same image
// that ships this shim — never a core-side seam.
const eeContentManifestPath = "/etc/stratt/ee-content.json"

// eeContent is the manifest's shape: what content this EE really contains, including
// transitive dependencies (declared=false), not a restatement of what was requested.
type eeContent struct {
	Collections []eeContentEntry `json:"collections"`
	Roles       []eeContentEntry `json:"roles"`
}

type eeContentEntry struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Declared bool   `json:"declared"`
}

// contentSummary renders a one-line statement of the EE's ansible content for the Run's
// event stream. D3 makes the image digest the single truth about what content a Run had —
// but a digest an operator cannot read answers no question during descent (§1.8), so the
// Run records the content by name and version.
//
// It ALWAYS produces a line, including when the manifest is missing: absence is the
// normal case for a drop-in ansible-builder-produced EE (a charter §3 commitment), and
// "unknown" is a materially different answer from "none" when someone is working out why
// a collection was not found. Injected reader so this is tested without a filesystem.
func contentSummary(read func(string) ([]byte, error)) string {
	raw, err := read(eeContentManifestPath)
	if err != nil {
		return fmt.Sprintf("ansible content: UNKNOWN — this EE ships no content manifest at %s (an ansible-builder-produced or hand-built image); what content the run had cannot be stated", eeContentManifestPath)
	}
	var c eeContent
	if err := json.Unmarshal(raw, &c); err != nil {
		return fmt.Sprintf("ansible content: UNREADABLE — %s did not decode (%v)", eeContentManifestPath, err)
	}
	render := func(kind string, entries []eeContentEntry) string {
		parts := make([]string, 0, len(entries))
		for _, e := range entries {
			s := e.Name + "==" + e.Version
			if !e.Declared {
				s += " (dependency)" // pulled in transitively — nobody asked for it directly
			}
			parts = append(parts, s)
		}
		slices.Sort(parts)
		return kind + ": " + strings.Join(parts, ", ")
	}
	switch {
	case len(c.Collections) == 0 && len(c.Roles) == 0:
		return "ansible content: none — this EE declares no collections or roles (ansible-core only)"
	case len(c.Roles) == 0:
		return "ansible content — " + render("collections", c.Collections)
	case len(c.Collections) == 0:
		return "ansible content — " + render("roles", c.Roles)
	}
	return "ansible content — " + render("collections", c.Collections) + "; " + render("roles", c.Roles)
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

// eventSeverity classifies a runner event for the descent. ansible's own failure
// events used to be forwarded at INFO — so a task that FAILED, or a host that was
// never even reachable, reached the operator tinted exactly like a task that worked.
// The level is the one severity signal every consumer reads (ADR-0117 g), so getting
// it wrong here is worse than not having it.
func eventSeverity(ev RunnerEvent) pluginv1.TaskEvent_Level {
	switch ev.Event {
	case "runner_on_failed", "runner_item_on_failed", "runner_on_unreachable", "runner_on_async_failed":
		return pluginv1.TaskEvent_LEVEL_ERROR
	case "runner_on_skipped", "runner_on_retry":
		return pluginv1.TaskEvent_LEVEL_WARN
	default:
		return pluginv1.TaskEvent_LEVEL_INFO
	}
}

// reasonCap bounds a carried reason. A module result can hold a whole command's
// stdout; the full text stays on the pod's stream, this is the descent's summary.
const reasonCap = 1024

// eventReason pulls the human cause out of a runner event's result, so a failure
// SAYS why rather than only that it happened (§1.8).
//
// This existed as a gap the app-cert demo walked straight into: the play could not
// SSH to its target, and the Run's event stream said `runner_on_unreachable` and
// nothing else. The actual sentence — "User appops not allowed because account is
// locked" — was in the event's own result payload the whole time, and diagnosing it
// meant reading sshd's logs on the target instead. The pod's logs are gone with the
// Job, so an operator would have had nothing.
func eventReason(ev RunnerEvent) string {
	res, ok := ev.EventData["res"].(map[string]any)
	if !ok {
		// Unreachable/failed events sometimes carry the text at the top level.
		res = ev.EventData
	}
	// Order matters: msg is ansible's own summary; stderr and module_stdout are what
	// a failing command actually printed; reason is the connection-plugin's word.
	for _, key := range []string{"msg", "reason", "stderr", "module_stdout", "stdout"} {
		if s, ok := res[key].(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				if len(s) > reasonCap {
					// Never truncate silently (§1.8).
					s = s[:reasonCap] + "… (truncated; full text on the Run's diagnostic stream)"
				}
				return s
			}
		}
	}
	return ""
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

// extractOutputs lifts the reserved `stratt_outputs` fact — a play's typed OUTPUTS for cross-Step
// binding, the sibling of `stratt_facets` (ADR-0084's fact-back) and deliberately a separate key.
//
// The two carry different things to different places and conflating them would be a §2.4-shaped
// mistake: a FACET is observed state projected onto an Entity under the Run's FacetWriteScope, and
// an OUTPUT is a value handed to a later Step in the same Workflow. A CSR is the clear case — it is
// not a fact about the host worth keeping in the graph, it is a request the next Step must sign,
// and writing it as a facet would put a short-lived artifact in a projection that outlives it.
//
// This is what makes the born-on-target certificate flow expressible at all (CERT-2): the target
// generates its key and CSR, publishes ONLY the CSR here, and the private key never crosses the
// wire (§2.5, the property cert-issuer's input Contract states in writing).
// factsOf lifts a task event's ansible_facts, shared by the facet and output extractors so both
// read the same place rather than each re-deriving it.
func factsOf(ev RunnerEvent) map[string]any {
	res, ok := ev.EventData["res"].(map[string]any)
	if !ok {
		return nil
	}
	facts, _ := res["ansible_facts"].(map[string]any)
	return facts
}

// extractOutputs returns the marshalled outputs and, when a play PUBLISHED something this
// function could not use, a diagnosis naming why. The second return value exists because the
// silent-drop version of this code cost a live debugging session: a play set `stratt_outputs`,
// every task reported ok, the Run folded SUCCEEDED with `outputs` NULL, and the failure finally
// surfaced two Steps later as
//
//	template path .steps.gather.outputs.csr: "csr" is not an object
//
// — a message about the CONSUMER, pointing nowhere near the producer that dropped the value.
// §1.8: hiding mechanism is the product, hiding failure is not. A play that publishes an output
// the shim discards must say so where the play ran.
func extractOutputs(facts map[string]any) ([]byte, string) {
	v, present := facts["stratt_outputs"]
	if !present {
		return nil, ""
	}
	so, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Sprintf("stratt_outputs must be a mapping of name -> value, got %T — "+
			"outputs bind as {{.steps.<step>.outputs.<field>}}, so a scalar has no field to bind", v)
	}
	if len(so) == 0 {
		return nil, "stratt_outputs is an empty mapping — nothing was published for a downstream Step to bind"
	}
	raw, err := json.Marshal(so)
	if err != nil {
		return nil, "stratt_outputs is not JSON-serialisable: " + err.Error()
	}
	return raw, ""
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

// outputFields lists the published output NAMES, sorted, for the shim's descent event. Names
// only, never values: an output can carry anything a play chose to publish and the event stream
// is not where that judgement gets made (§2.5).
func outputFields(raw []byte) []string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return slices.Sorted(maps.Keys(m))
}
