// Package desiredstate reconciles the Git-declared desired state into the
// graph (charter §1.2: desired state lives in Git; drift is the diff). The
// declarable unit in Phase 1 is the View (§2.1: CaC-declared); Intents and
// Assignments join in Phase 2.
//
// The same plan/apply engine serves the API (POST /desired-state/plan|apply,
// used by the stratt CLI) and the in-process reconcile Controller — one
// semantics, two entry points (§1.6).
//
// Phase-2 constraint to carry forward (charter-guardian, §2.1): when
// Assignments land, the compiler must reject an Assignment referencing a
// View that is not cac-declared — otherwise desired state escapes Git.
package desiredstate

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"

	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/policy"
	"github.com/dstout-devops/stratt/core/internal/provision"
	"github.com/dstout-devops/stratt/core/internal/rules"
	"github.com/dstout-devops/stratt/core/internal/template"
	"github.com/dstout-devops/stratt/types"
)

// Declaration is one declared View. JSON tags mirror the API's
// ViewDeclaration wire schema so the CLI can send declarations verbatim.
type Declaration struct {
	Name     string             `json:"name"`
	Selector types.ViewSelector `json:"selector"`
}

// Declarations is the full declared desired state — every kind the repo can
// declare (Views since slice 2; CredentialRef pointers since ADR-0009;
// Intents/Assignments arrive in Phase 2).
type Declarations struct {
	Views          []Declaration         `json:"views"`
	CredentialRefs []types.CredentialRef `json:"credentialRefs"`
	Connectors     []types.Connector     `json:"connectors"` // ADR-0103: runtime-registered Connectors
	Actuators      []types.Actuator      `json:"actuators"`  // ADR-0103: runtime-registered plugin Actuators
	// CapabilityBindings select which verified provider fulfils a capability class for a given
	// Intent kind (ADR-0110 D3) — a CaC declaration form, not a Named Kind (§2 frozen).
	CapabilityBindings []types.CapabilityBinding `json:"capabilityBindings"`
	Triggers           []types.Trigger           `json:"triggers"`
	Workflows          []types.Workflow          `json:"workflows"`
	Emitters           []types.Emitter           `json:"emitters"`
	Baselines          []types.Baseline          `json:"baselines"`
	MCPServers         []types.MCPServer         `json:"mcpServers"`
	Intents            []types.Intent            `json:"intents"`
	Assignments        []types.Assignment        `json:"assignments"`
	Blueprints         []types.Blueprint         `json:"blueprints"`
	NotifySinks        []types.Sink              `json:"notifySinks"`
	Subscriptions      []types.Subscription      `json:"subscriptions"`
	Sites              []types.Site              `json:"sites"`
	Cells              []types.Cell              `json:"cells"`
	SCIMIdPs           []types.SCIMIdP           `json:"scimIdps"`
	// AdmissionControls are the estate's admission policy (ADR-0073 §7.4b): CEL
	// predicates over each declaration's manifest, evaluated at load through the
	// PDP port. A deny rejects the declaration (§7.4c).
	AdmissionControls []types.Control `json:"admissionControls,omitempty"`
}

// Declared kinds appearing in plans.
const (
	KindView          = "view"
	KindCredentialRef = "credential-ref"
	KindConnector     = "connector"
	KindActuator      = "actuator"
	// KindCapabilityBinding is a CaC declaration form (ADR-0110), not a Named Kind (§2 frozen).
	KindCapabilityBinding = "capability-binding"
	KindTrigger           = "trigger"
	KindWorkflow          = "workflow"
	KindEmitter           = "emitter"
	KindBaseline          = "baseline"
	KindMCPServer         = "mcp-server"
	KindIntent            = "intent"
	KindAssignment        = "assignment"
	KindBlueprint         = "blueprint"
	KindNotifySink        = "notify-sink"
	KindSubscription      = "subscription"
	KindSite              = "site"
	KindCell              = "cell"
	KindSCIMIdP           = "scim-idp"
)

// Action is what reconciliation will do (or did) to one declared object.
type Action string

const (
	ActionCreate Action = "create"
	ActionUpdate Action = "update"
	// ActionAdopt promotes an api-declared View into the desired state
	// (ownership transfers to cac; the selector may change in the same step).
	ActionAdopt Action = "adopt"
	ActionNoop  Action = "noop"
	// ActionDelete prunes a cac-declared View absent from the declarations.
	// api-declared Views are never pruned.
	ActionDelete Action = "delete"
)

// PlanEntry is the plan for one declared object. JSON tags mirror the wire
// schema.
type PlanEntry struct {
	// Kind is the declared kind: "view" | "credential-ref".
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action Action `json:"action"`
	// MemberCount is the live Entity count the relevant selector matches now
	// (the desired selector; for deletes, the outgoing one) — blast-radius
	// visibility before anything executes (§4.3).
	MemberCount int64               `json:"memberCount"`
	OldSelector *types.ViewSelector `json:"oldSelector,omitempty"`
	NewSelector *types.ViewSelector `json:"newSelector,omitempty"`
	// ParamDependent marks a parametrized View (ADR-0024) whose membership
	// depends on launch params — MemberCount is not meaningful (it binds at
	// launch, not reconcile) and is left 0 rather than a misleading count.
	ParamDependent bool `json:"paramDependent,omitempty"`
	// Error carries a per-View apply failure (apply continues past it).
	Error string `json:"error,omitempty"`
}

// Plan is the ordered reconciliation plan.
type Plan struct {
	Entries []PlanEntry `json:"entries"`
}

// Changes reports how many entries are not noops.
func (p Plan) Changes() int {
	n := 0
	for _, e := range p.Entries {
		if e.Action != ActionNoop {
			n++
		}
	}
	return n
}

// PruneStats reports, per declared kind, how many currently-cac objects this
// plan would delete out of how many exist. Every current cac object appears
// in a plan as exactly one of noop/update/delete, so both numbers fall out
// of the entries. Per-kind so one kind's bulk (e.g. many Views) can never
// mask the total disappearance of another (e.g. every CredentialRef).
func (p Plan) PruneStats() map[string][2]int { // kind → {deletes, cacTotal}
	out := map[string][2]int{}
	for _, e := range p.Entries {
		kind := e.Kind
		if kind == "" {
			kind = KindView
		}
		s := out[kind]
		switch e.Action {
		case ActionDelete:
			s[0]++
			s[1]++
		case ActionNoop, ActionUpdate:
			s[1]++
		}
		out[kind] = s
	}
	return out
}

// ── declarations directory ──────────────────────────────────────────────────

// yaml-side shapes: yaml.v3 does not read json tags, and Equals must become
// canonical JSON for the selector document.
type declFile struct {
	Name     string       `yaml:"name"`
	Selector declSelector `yaml:"selector"`
}
type declSelector struct {
	Kinds  []string          `yaml:"kinds"`
	Labels map[string]string `yaml:"labels"`
	Facets []declFacet       `yaml:"facets"`
	// Relations selects by TOPOLOGY (ADR-0059 decision 6) — "the hosts in the DMZ", "the
	// job templates labelled prod". types.ViewSelector has carried this since ADR-0059
	// and the CaC decoder did not expose it, so a declared View could select by kind,
	// label and facet but never by an edge; found by ADR-0132, whose whole point is that
	// an operator's AWX label vocabulary becomes a View this way.
	Relations []declRelation `yaml:"relations"`
}
type declRelation struct {
	Type         string            `yaml:"type"`
	TargetKind   string            `yaml:"targetKind"`
	TargetLabels map[string]string `yaml:"targetLabels"`
}
type declFacet struct {
	Namespace string `yaml:"namespace"`
	Path      string `yaml:"path"`
	Equals    any    `yaml:"equals"`
}

// ParseDir reads the declarations checkout: every *.yaml/*.yml under
// <root>/views plus, when present, <root>/credential-refs. A missing views
// directory is an error, not an empty set — an empty set prunes every
// cac-declared View, and a mistyped path must never look like one.
// (credential-refs/ is optional: repos predating ADR-0009 stay valid.)
// ParseDir loads and validates the estate declarations. decider is the PDP port
// for the admission PEP (ADR-0073 §7.4c): when the estate declares an admission
// policy (admission/) and a decider is supplied, every OTHER declaration's
// manifest is admitted through the port and a deny rejects the load, fail-closed
// (§1.8). A nil decider skips admission (e.g. a boot-time authz-only load).
func ParseDir(root string, decider policy.Decider) (Declarations, error) {
	var out Declarations

	// Admission policy first: it governs every other declaration (§3
	// Kyverno-for-config). Controls are validated at load (CEL-only, allow/deny).
	admissionDecls, err := parseKind(filepath.Join(root, "admission"), true, parseAdmissionFile)
	if err != nil {
		return out, err
	}
	for _, a := range admissionDecls {
		out.AdmissionControls = append(out.AdmissionControls, a.Controls...)
	}
	if len(out.AdmissionControls) > 0 && decider != nil {
		if err := admitEstate(root, out.AdmissionControls, decider); err != nil {
			return out, err
		}
	}

	views, err := parseKind(filepath.Join(root, "views"), false, parseViewFile)
	if err != nil {
		return out, err
	}
	out.Views = views
	sort.Slice(out.Views, func(i, j int) bool { return out.Views[i].Name < out.Views[j].Name })

	refs, err := parseKind(filepath.Join(root, "credential-refs"), true, parseCredentialRefFile)
	if err != nil {
		return out, err
	}
	out.CredentialRefs = refs
	sort.Slice(out.CredentialRefs, func(i, j int) bool { return out.CredentialRefs[i].Name < out.CredentialRefs[j].Name })

	connectors, err := parseKind(filepath.Join(root, "connectors"), true, parseConnectorFile)
	if err != nil {
		return out, err
	}
	out.Connectors = connectors
	sort.Slice(out.Connectors, func(i, j int) bool { return out.Connectors[i].Name < out.Connectors[j].Name })

	actuatorDecls, err := parseKind(filepath.Join(root, "actuators"), true, parseActuatorFile)
	if err != nil {
		return out, err
	}
	out.Actuators = actuatorDecls
	sort.Slice(out.Actuators, func(i, j int) bool { return out.Actuators[i].Name < out.Actuators[j].Name })

	// An input Contract belongs to the TOOL, not to the local name this estate gives one
	// of its Actuators, so every declaration that names an Actuator is validated with the
	// name → pluginIdentity map in hand (ADR-0117 D3a; see
	// contract.ValidateActuatorParamsFor). Actuators are parsed BEFORE the kinds that
	// reference them, which is what makes this available here.
	actuatorIDs := WithActuatorIdentities(actuatorIdentities(actuatorDecls))

	capBindings, err := parseKind(filepath.Join(root, "capability-bindings"), true, parseCapabilityBindingFile)
	if err != nil {
		return out, err
	}
	out.CapabilityBindings = capBindings
	sort.Slice(out.CapabilityBindings, func(i, j int) bool { return out.CapabilityBindings[i].Name < out.CapabilityBindings[j].Name })

	triggers, err := parseKind(filepath.Join(root, "triggers"), true,
		func(path string, raw []byte) (string, types.Trigger, error) {
			return parseTriggerFile(path, raw, actuatorIDs)
		})
	if err != nil {
		return out, err
	}
	out.Triggers = triggers
	sort.Slice(out.Triggers, func(i, j int) bool { return out.Triggers[i].Name < out.Triggers[j].Name })

	workflows, err := parseKind(filepath.Join(root, "workflows"), true,
		func(path string, raw []byte) (string, types.Workflow, error) {
			return parseWorkflowFile(path, raw, actuatorIDs)
		})
	if err != nil {
		return out, err
	}
	out.Workflows = workflows
	sort.Slice(out.Workflows, func(i, j int) bool { return out.Workflows[i].Name < out.Workflows[j].Name })

	emitters, err := parseKind(filepath.Join(root, "emitters"), true, parseEmitterFile)
	if err != nil {
		return out, err
	}
	out.Emitters = emitters
	sort.Slice(out.Emitters, func(i, j int) bool { return out.Emitters[i].Name < out.Emitters[j].Name })

	sites, err := parseKind(filepath.Join(root, "sites"), true, parseSiteFile)
	if err != nil {
		return out, err
	}
	out.Sites = sites
	sort.Slice(out.Sites, func(i, j int) bool { return out.Sites[i].Name < out.Sites[j].Name })

	cells, err := parseKind(filepath.Join(root, "cells"), true, parseCellFile)
	if err != nil {
		return out, err
	}
	out.Cells = cells
	sort.Slice(out.Cells, func(i, j int) bool { return out.Cells[i].Name < out.Cells[j].Name })
	if err := validateCellSet(out.Cells); err != nil {
		return out, err
	}

	scimIdps, err := parseKind(filepath.Join(root, "scim"), true, parseScimFile)
	if err != nil {
		return out, err
	}
	out.SCIMIdPs = scimIdps
	sort.Slice(out.SCIMIdPs, func(i, j int) bool { return out.SCIMIdPs[i].Name < out.SCIMIdPs[j].Name })

	notifySinks, err := parseKind(filepath.Join(root, "notify-sinks"), true, parseNotifySinkFile)
	if err != nil {
		return out, err
	}
	out.NotifySinks = notifySinks
	sort.Slice(out.NotifySinks, func(i, j int) bool { return out.NotifySinks[i].Name < out.NotifySinks[j].Name })

	subscriptions, err := parseKind(filepath.Join(root, "subscriptions"), true, parseSubscriptionFile)
	if err != nil {
		return out, err
	}
	out.Subscriptions = subscriptions
	sort.Slice(out.Subscriptions, func(i, j int) bool { return out.Subscriptions[i].Name < out.Subscriptions[j].Name })

	baselines, err := parseKind(filepath.Join(root, "baselines"), true,
		func(path string, raw []byte) (string, types.Baseline, error) {
			return parseBaselineFile(path, raw, actuatorIDs)
		})
	if err != nil {
		return out, err
	}
	out.Baselines = baselines
	sort.Slice(out.Baselines, func(i, j int) bool { return out.Baselines[i].Name < out.Baselines[j].Name })

	mcpServers, err := parseKind(filepath.Join(root, "mcp-servers"), true, parseMCPServerFile)
	if err != nil {
		return out, err
	}
	out.MCPServers = mcpServers
	sort.Slice(out.MCPServers, func(i, j int) bool { return out.MCPServers[i].Name < out.MCPServers[j].Name })

	intents, err := parseKind(filepath.Join(root, "intents"), true, parseIntentFile)
	if err != nil {
		return out, err
	}
	out.Intents = intents
	sort.Slice(out.Intents, func(i, j int) bool {
		if out.Intents[i].Name != out.Intents[j].Name {
			return out.Intents[i].Name < out.Intents[j].Name
		}
		return out.Intents[i].Version < out.Intents[j].Version
	})
	if err := validateSingleIntentVersion(out.Intents); err != nil {
		return out, err
	}

	assignments, err := parseKind(filepath.Join(root, "assignments"), true, parseAssignmentFile)
	if err != nil {
		return out, err
	}
	out.Assignments = assignments
	sort.Slice(out.Assignments, func(i, j int) bool { return out.Assignments[i].Name < out.Assignments[j].Name })

	blueprints, err := parseKind(filepath.Join(root, "blueprints"), true, parseBlueprintFile)
	if err != nil {
		return out, err
	}
	out.Blueprints = blueprints
	sort.Slice(out.Blueprints, func(i, j int) bool {
		if out.Blueprints[i].Name != out.Blueprints[j].Name {
			return out.Blueprints[i].Name < out.Blueprints[j].Name
		}
		return out.Blueprints[i].Version < out.Blueprints[j].Version
	})
	if err := checkTriggerLaunchInputs(out); err != nil {
		return out, err
	}
	if err := checkBlueprintParamNames(out); err != nil {
		return out, err
	}
	if err := checkProvisioningBuildInputs(out); err != nil {
		return out, err
	}
	return out, nil
}

// checkTriggerLaunchInputs validates a SCHEDULE Trigger's inputs against the declared inputs
// of the Workflow it launches (ADR-0118 D5).
//
// It runs here, after every kind is parsed, because a Trigger is parsed BEFORE the Workflows
// it may reference — so this is the earliest point both documents exist. That earliness is the
// whole reason to do it: a schedule has no firing event, so its inputs are literal values
// (event templates are rejected at declaration, ADR-0024 D7), which means they can be checked
// in Git review instead of when the schedule first fires at 3am.
//
// EVENT Triggers are deliberately excluded. Their inputs carry {{.event.x}} bindings that
// resolve only against a real payload, so the placeholder is not the value the schema must
// accept — the same reasoning ADR-0024 D4 recorded for Actuator params, and they are validated
// after substitution by the launch chokepoint instead. A Trigger naming a Workflow that does
// not exist is likewise left alone: that is the compiler's existing cross-reference check, and
// duplicating it here would give two different errors for one mistake.
func checkTriggerLaunchInputs(decls Declarations) error {
	byName := make(map[string]types.Workflow, len(decls.Workflows))
	for _, w := range decls.Workflows {
		byName[w.Name] = w
	}
	for _, t := range decls.Triggers {
		if t.WorkflowName == "" || t.Kind != types.TriggerSchedule || len(t.Inputs) == 0 {
			continue
		}
		wf, ok := byName[t.WorkflowName]
		if !ok {
			continue // unresolved ref: the compiler's job, not a second opinion here
		}
		if _, err := contract.ResolveLaunchInputs(wf.Name, wf.Inputs, t.Inputs); err != nil {
			return fmt.Errorf("trigger %s: inputs do not satisfy workflow %s: %w", t.Name, wf.Name, err)
		}
	}
	return nil
}

// checkBlueprintParamNames validates that every KEY a Blueprint passes to a Workflow is a
// declared input of that Workflow, and that no required input is left unsupplied (ADR-0118 D3).
// Covers a route's `remediationParams` and the Blueprint's own `removeParams`.
//
// The compiler already performs the full check, against the SUBSTITUTED values. This is not a
// duplicate of it — it is the half of that check which does not need substitution, moved to the
// earliest point it can run (§1.8: declaration > compile > launch). Keys and required-ness are
// literal in the declaration; only value TYPES depend on the resolved spec, which needs an
// Assignment and therefore belongs to the compile.
//
// Earliness is the entire payoff, and it is not theoretical: the compiler's check lives behind
// Compile(), whose only test driver skips without a live Postgres — so a mistyped param key in
// the reference estate passed `task ci` and would have failed at the first real compile. Here it
// fails in front of whoever edits the Blueprint.
//
// A Blueprint naming a Workflow that does not exist is left alone: that is the compiler's
// cross-reference check, and two errors for one mistake is worse than one.
func checkBlueprintParamNames(decls Declarations) error {
	byName := make(map[string]types.Workflow, len(decls.Workflows))
	for _, w := range decls.Workflows {
		byName[w.Name] = w
	}
	check := func(what, workflow string, params map[string]any) error {
		if workflow == "" || len(params) == 0 {
			return nil
		}
		wf, ok := byName[workflow]
		if !ok {
			return nil
		}
		// Only KEYS and required-ness are checked. A `{{.spec.x}}` placeholder is not the value
		// the schema will see, so its TYPE cannot be judged here — the same reasoning ADR-0024
		// D4 records for Actuator params, and why the typed check stays at compile.
		declared, err := contract.InputNames(wf.Inputs)
		if err != nil {
			return fmt.Errorf("%s: workflow %s inputs: %w", what, wf.Name, err)
		}
		required, err := contract.RequiredNames(wf.Inputs)
		if err != nil {
			return fmt.Errorf("%s: workflow %s inputs: %w", what, wf.Name, err)
		}
		names := make([]string, 0, len(declared))
		for n := range declared {
			names = append(names, n)
		}
		sort.Strings(names)
		keys := make([]string, 0, len(params))
		for k := range params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !declared[k] {
				return fmt.Errorf("%s: %q is not a declared input of workflow %s (declared: %v)",
					what, k, wf.Name, names)
			}
		}
		for _, req := range required {
			if _, ok := params[req]; !ok {
				return fmt.Errorf("%s: workflow %s requires input %q, which this Blueprint does not pass",
					what, wf.Name, req)
			}
		}
		return nil
	}
	for _, b := range decls.Blueprints {
		if err := check(fmt.Sprintf("blueprint %s@%d removeParams", b.Name, b.Version),
			b.RemoveWorkflow, b.RemoveParams); err != nil {
			return err
		}
		for i, r := range b.Routes {
			if err := check(fmt.Sprintf("blueprint %s@%d route %d remediationParams", b.Name, b.Version, i),
				r.RemediationWorkflow, r.RemediationParams); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkProvisioningBuildInputs validates that every Workflow a provider advertises as a gated
// BUILD Workflow can actually accept what the provisioning reconcile will hand it (ADR-0120 D3).
//
// Provisioning has no compile — it is a sibling reconcile (ADR-0058) — so this is the equivalent of
// the compile-time cross-check a Blueprint route gets, at the earliest point it can run (§1.8:
// declaration > compile > launch). It matters more here than for a route, because the params are
// CORE-GENERATED: a mismatch is always a Workflow authoring error, and the author is the only
// person who can fix it.
//
// IT CHECKS EVERY CANDIDATE, NOT THE WINNER, and that is deliberately stronger than the reconcile.
// Which provider wins depends on the daemon's active environment and on which providers are
// VERIFIED — runtime state Git cannot see (ADR-0110 D3, ADR-0113 D2). So every Workflow named in any
// provider's `provisions` map for a kind must fit. `estate/actuators/awsec2.yaml` and
// `vcenter.yaml` both advertise a Compute builder; a fix that satisfied only the one bound in this
// environment would break the other on a binding change, which is precisely the moment nobody is
// looking at the build Workflow.
//
// The expected param set is taken from provision.BuildLaunchParams itself rather than a list
// duplicated here, so the check cannot drift from what the reconcile actually sends.
func checkProvisioningBuildInputs(decls Declarations) error {
	byName := make(map[string]types.Workflow, len(decls.Workflows))
	for _, w := range decls.Workflows {
		byName[w.Name] = w
	}
	// Every Workflow any provider advertises for a kind, keyed by the bare kind ("Compute").
	builders := map[string][]string{}
	add := func(provisions map[string]string) {
		for kind, wf := range provisions {
			if !slices.Contains(builders[kind], wf) {
				builders[kind] = append(builders[kind], wf)
			}
		}
	}
	for _, a := range decls.Actuators {
		add(a.Provisions)
	}
	for _, c := range decls.Connectors {
		add(c.Provisions)
	}
	// The teardown counterpart (ADR-0114 D4's `decommissions:` map). Collected the same way and
	// checked by the same rules: until the decommission Finding carried a launch spec, a mismatched
	// teardown Workflow was invisible because nothing launched it from the Finding at all.
	teardowns := map[string][]string{}
	addT := func(decommissions map[string]string) {
		for kind, wf := range decommissions {
			if !slices.Contains(teardowns[kind], wf) {
				teardowns[kind] = append(teardowns[kind], wf)
			}
		}
	}
	for _, a := range decls.Actuators {
		addT(a.Decommissions)
	}
	for _, c := range decls.Connectors {
		addT(c.Decommissions)
	}
	for k := range builders {
		sort.Strings(builders[k])
	}
	for k := range teardowns {
		sort.Strings(teardowns[k])
	}

	// A provisions map naming a Workflow that does not exist is caught HERE and nowhere else.
	// validateProvisions only checks the entry is non-empty, and the reconcile simply copies the name
	// onto the Finding — so `provisions: {Subnet: opentofu-subnet-build}` against a Workflow nobody
	// declared produced a build Finding offering a Workflow that cannot be launched, discovered by an
	// operator at the gate (§1.8). A provider must not advertise a capability it has no Workflow for.
	for kind, wfNames := range builders {
		for _, wfName := range wfNames {
			if _, ok := byName[wfName]; !ok {
				return fmt.Errorf(
					"a provisioning provider advertises workflow %q as its %s builder, but no such "+
						"Workflow is declared — the build Finding would name a Workflow nobody can "+
						"launch. Declare it, or drop the provisions entry: advertising a capability "+
						"with no Workflow behind it is a promise the estate cannot keep", wfName, kind)
			}
		}
	}
	// Same rule for teardown, and validateDecommissions is the same partial check validateProvisions
	// was: it verifies the entry is non-empty and nothing more. A dangling teardown target is worse
	// than a dangling builder, because the operator meets it while trying to destroy something.
	for kind, wfNames := range teardowns {
		for _, wfName := range wfNames {
			if _, ok := byName[wfName]; !ok {
				return fmt.Errorf(
					"a provisioning provider advertises workflow %q as its %s teardown, but no such "+
						"Workflow is declared — the decommission Finding would name a Workflow nobody "+
						"can launch. Declare it, or drop the decommissions entry (ADR-0114 D4)", wfName, kind)
			}
		}
	}

	for _, in := range decls.Intents {
		// The generated param set differs per provisioning shape: a fleet instance has an ordinal
		// and stratt.intent/instance; a named singleton has a per-kind (intentKind, name) key and
		// stratt.intent/singleton. Taken from the same functions the reconcile calls, so neither can
		// drift from what is actually sent.
		var supplied map[string]any
		var kind, unit string
		switch {
		case in.Kind == types.IntentCompute:
			pin, err := provision.FromIntent(in)
			if err != nil {
				return fmt.Errorf("intent %s: %w", in.Name, err)
			}
			// One representative instance is enough: the ordinal changes the VALUES, never the
			// KEY SET, and this check is about keys.
			supplied = provision.BuildLaunchParams(pin, provision.Instance{
				Name:    provision.InstanceName(pin.Spec.NamePrefix, 1, pin.Spec.Count),
				Intent:  in.Name,
				Ordinal: 1,
			})
			kind, unit = "Compute", "instance"
		case types.SingletonIntentKinds[in.Kind]:
			sin, err := provision.FromSingletonIntent(in)
			if err != nil {
				return fmt.Errorf("intent %s: %w", in.Name, err)
			}
			supplied = provision.SingletonLaunchParams(sin, provision.Instance{
				Name:   provision.SingletonKey(in.Kind, in.Name),
				Intent: in.Name,
			})
			kind, unit = strings.TrimPrefix(in.Kind, "Intent/"), "singleton"
		default:
			continue // not a provisioning kind
		}

		for _, wfName := range builders[kind] {
			what := fmt.Sprintf("intent %s builds via workflow %s", in.Name, wfName)
			if err := checkAdvertisedWorkflow(what, byName[wfName], in, supplied, unit, "build"); err != nil {
				return err
			}
		}
		// The teardown half (ADR-0114 D4). Same checks, same reason: the decommission reconcile
		// now sends a launch spec, so an advertised teardown Workflow that cannot accept it is a
		// destructive act an operator discovers at the gate. Only Compute has a teardown reach-path
		// today (whole-Intent withdrawal for singletons is ADR-0114's own booked follow-up), so a
		// singleton kind simply has no entry here rather than a special case.
		for _, wfName := range teardowns[kind] {
			what := fmt.Sprintf("intent %s tears down via workflow %s", in.Name, wfName)
			// Representative values: this check is about the KEY SET, and a placeholder identity
			// carries the same keys a real one does.
			td := provision.TeardownLaunchParams(in.Name,
				provision.Instance{Name: teardownProbeName(supplied), Intent: in.Name, Ordinal: 1},
				"provider.identity", "probe")
			if err := checkAdvertisedWorkflow(what, byName[wfName], in, td, unit, "tear down"); err != nil {
				return err
			}
		}
	}
	return nil
}

// teardownProbeName reuses the build side's derived instance name for the teardown probe, so the
// hardcoded-literal check inside checkAdvertisedWorkflow compares against a realistic value.
func teardownProbeName(supplied map[string]any) string {
	if s, ok := supplied["instance"].(string); ok {
		return s
	}
	if s, ok := supplied["singleton"].(string); ok {
		return s
	}
	return ""
}

// checkAdvertisedWorkflow is the per-(Intent, advertised Workflow) check, shared by the build and
// teardown halves (ADR-0120 D3, extended to ADR-0114 D4).
//
// Shared rather than duplicated because every one of these properties was found broken on the build
// side, and a teardown is the MORE dangerous act: the gate check in particular protects a destroy.
// `act` is the verb for the message ("build" / "tear down") — the only thing that differs.
func checkAdvertisedWorkflow(what string, wf types.Workflow, in types.Intent, supplied map[string]any, unit, act string) error {
	declared, err := contract.InputNames(wf.Inputs)
	if err != nil {
		return fmt.Errorf("%s: inputs: %w", what, err)
	}
	if len(declared) == 0 {
		return fmt.Errorf(
			"%s, which declares no `inputs` — so the reconcile cannot tell it WHICH %s to %s, and a "+
				"%s it cannot name is unreachable through the gated path (ADR-0120 D2). "+
				"Declare inputs for: %v", what, unit, act, unit, sortedKeys(supplied))
	}
	names := make([]string, 0, len(declared))
	for n := range declared {
		names = append(names, n)
	}
	sort.Strings(names)
	// The direction here inverted with ADR-0123 D3, and the old direction is why `placement` was
	// declared by every builder and bound by none.
	//
	// It used to require every SUPPLIED key be declared. Combined with
	// `additionalProperties: false` that forced a builder to declare each param the reconcile
	// might send whether or not it used one — so an input accepted and silently dropped was
	// structurally indistinguishable from a consumed one, and nothing anywhere said so.
	//
	// Now core sends only what the builder DECLARES (provision.FilterToDeclared at the reconcile),
	// so the rule is the other way round: a declared input must be one the reconcile can actually
	// supply. A builder asking for something core never sends is a launch that can never satisfy
	// its own required set.
	for _, k := range names {
		if _, ok := supplied[k]; !ok {
			return fmt.Errorf(
				"%s, but it declares input %q, which the provisioning reconcile never supplies "+
					"(it sends: %v). An input core cannot fill is either a typo or a launch this "+
					"%s can never satisfy (ADR-0123 D3)", what, k, sortedKeys(supplied), act)
		}
	}
	// The correlation-critical inputs must be declared, because omitting one is INVISIBLE: the
	// build succeeds and the Entity appears, but without `labels` it carries no
	// stratt.intent/instance and the Finding it was launched from never resolves — so the same
	// gated act is surfaced forever (the ADR-0120 defect, reachable again the moment declaring
	// them became optional).
	// `labels` and `projectKind` only — NOT the unit key. The correlation key rides INSIDE labels
	// (stratt.intent/instance, stratt.intent/singleton), so labels is the load-bearing one; the
	// unit-name input is a convenience a builder may legitimately not use. Requiring it here
	// contradicted the bound-check below for every singleton builder, which is how this rule was
	// caught being wrong.
	for _, k := range []string{"projectKind", "labels"} {
		if _, sent := supplied[k]; sent && !declared[k] {
			return fmt.Errorf(
				"%s, but it does not declare %q. That one is not optional: without it the %s "+
					"runs and appears to succeed while the Finding it came from never resolves, so "+
					"the same act is surfaced forever (ADR-0120 D3, ADR-0123 D3)", what, k, act)
		}
	}
	// And every declared input must be BOUND by some Step — the half that makes
	// accepted-but-dropped unshippable rather than merely discouraged (ADR-0123 D3).
	if unbound := unboundInputs(wf, names); len(unbound) > 0 {
		return fmt.Errorf(
			"%s, but it declares input(s) %v that no Step binds via {{.launch.*}}. A declared input "+
				"nothing consumes is accepted and silently dropped, which is exactly how a declared "+
				"`placement` reached no provider for as long as it did (ADR-0123 D3). Bind it, or "+
				"stop declaring it", what, unbound)
	}
	if label, bad := hardcodedCorrelationLabel(wf); bad {
		return fmt.Errorf(
			"%s, but that workflow hardcodes the correlation label %q in a step. It must forward "+
				"{{.launch.labels}} instead: the reconcile derives the exact key and value it will "+
				"later match on, and a hand-written one that is wrong still BUILDS — the Entity "+
				"appears, the Finding never resolves, and the same gated act is surfaced forever",
			what, label)
	}
	// §5 Flow 1: a build is GATED, never auto-run, and a teardown all the more so. The gate lives
	// in the Workflow as an approval Step, so a Workflow without one converts "launch this" into
	// "this has happened" with no approval anywhere on the path. Every advertised Workflow already
	// carries one, which is exactly why this is worth pinning: the invariant holds by convention,
	// and convention is what the rest of this check keeps finding broken.
	if !hasApprovalGate(wf) {
		return fmt.Errorf(
			"%s, but that workflow declares no approval gate Step — a provisioning act is "+
				"GATED, never auto-run (§5 Flow 1). Launching it would %s real "+
				"infrastructure with no approval on the path. Add a Step with a `gate:`",
			what, act)
	}
	if lit, why, bad := hardcodedInstanceLiteral(wf, in); bad {
		return fmt.Errorf(
			"%s, but that workflow hardcodes %q in a step — %s. It is shared by every "+
				"declaration of its kind, so it must be identity-BLIND and take all of it from "+
				"{{.launch.*}}. Binding the top-level params is not enough: the literal that "+
				"motivated this check sat inside an opaque provider manifest, where building the "+
				"SECOND declaration applied a resource under the FIRST one's name and config — "+
				"an overwrite, not a failure (ADR-0120 D3)",
			what, lit, why)
	}
	required, err := contract.RequiredNames(wf.Inputs)
	if err != nil {
		return fmt.Errorf("%s: inputs: %w", what, err)
	}
	for _, req := range required {
		if _, ok := supplied[req]; !ok {
			return fmt.Errorf(
				"%s, which requires input %q — but the provisioning reconcile never supplies that, "+
					"so every launch would be refused. It sends: %v",
				what, req, sortedKeys(supplied))
		}
	}
	return nil
}

// unboundInputs reports which declared inputs no Step binds via {{.launch.*}} (ADR-0123 D3).
//
// Sound because a binding can only be a literal token: ADR-0083 D5 rules out computed paths, so
// scanning the Steps for `{{.launch.<name>` finds every possible consumer. A nested binding
// (`{{.launch.params.region}}`) counts as consuming `params`, which is right — the Workflow does
// use it.
func unboundInputs(w types.Workflow, declared []string) []string {
	bound := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			for _, ref := range template.LaunchFields(t) {
				bound[ref] = true
			}
		case map[string]any:
			for _, val := range t {
				walk(val)
			}
		case []any:
			for _, val := range t {
				walk(val)
			}
		}
	}
	for _, st := range w.Steps {
		walk(st.Params)
	}
	var out []string
	for _, name := range declared {
		if !bound[name] {
			out = append(out, name)
		}
	}
	return out
}

// hardcodedCorrelationLabel reports the first `stratt.intent/*` key a build Workflow writes BY HAND,
// which a build Workflow must never do (ADR-0120 D3).
//
// This guards a bug class, not just the bug that motivated it. The correlation label is how the next
// reconcile decides a unit is built: it must be the exact key the planner reads
// (stratt.intent/instance for a fleet, stratt.intent/singleton for a named singleton) carrying the
// exact value the planner derived. Hand-writing it can get the KEY wrong or the VALUE wrong, and both
// failures look like success — the build runs, the Entity appears, and the Finding never resolves, so
// the reconcile keeps surfacing the same gated build forever.
//
// estate/workflows/vsphere-subnet-build.yaml shipped exactly that: it projected
// `stratt.intent/subnet`, a key nothing reads. Forwarding {{.launch.labels}} makes the failure
// impossible instead of merely absent, because the reconcile owns both halves.
func hardcodedCorrelationLabel(w types.Workflow) (string, bool) {
	const prefix = "stratt.intent/"
	var walk func(v any) (string, bool)
	walk = func(v any) (string, bool) {
		switch t := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if strings.HasPrefix(k, prefix) {
					return k, true
				}
				if found, ok := walk(t[k]); ok {
					return found, true
				}
			}
		case []any:
			for _, e := range t {
				if found, ok := walk(e); ok {
					return found, true
				}
			}
		}
		return "", false
	}
	for _, st := range w.Steps {
		if found, ok := walk(map[string]any(st.Params)); ok {
			return found, true
		}
	}
	return "", false
}

// hasApprovalGate reports whether a Workflow contains a human-approval Step (§2 Gates). A Policy Step
// is deliberately NOT accepted: it is an automated decision point, and §5 Flow 1's gate is a human
// one. A builder wanting both declares both.
func hasApprovalGate(w types.Workflow) bool {
	for _, st := range w.Steps {
		if st.Gate != nil {
			return true
		}
	}
	return false
}

// hardcodedInstanceLiteral reports the first string literal in a build Workflow's steps that belongs
// to a SPECIFIC declaration of the kind it builds — the Intent's own name, or one of the opaque
// `params` values the Intent declares. It returns the literal and a phrase naming what it collided
// with (ADR-0120 D3).
//
// This is the sibling of hardcodedCorrelationLabel, guarding the other half of the same class. That
// one catches a builder writing the correlation key by hand; this one catches a builder that knows
// WHICH declaration it is building. A builder is shared by every Intent of its kind, so any per-
// declaration literal means it builds one of them and silently mis-builds the rest.
//
// It exists because parameterizing the top level was not enough. estate/workflows/subnet-build.yaml
// bound {{.launch.name}} and {{.launch.params.cidr}} in its step params while its nested
// spec.forProvider.manifest — the manifest provider-kubernetes actually applies — still read
// `subnet-app-subnet` and `10.30.0.0/24`. app-subnet and dmz-subnet are both Intent/Subnet and both
// route here, so building dmz-subnet would have applied a ConfigMap under app-subnet's NAME carrying
// app-subnet's CIDR: an overwrite of the other subnet's resource, not a visible failure. vlan-build
// had the identical shape (`vlan-net-vlan`, vid "100"), latent only because exactly one Intent/Vlan
// is declared — a builder that works while one declaration exists is a defect awaiting the second.
//
// The walk is deliberately literal-only and needs no rendering: anything correctly parameterized is a
// {{.launch.*}} token, so a bare occurrence of a declaration's identity is by construction wrong.
// Names match on SUBSTRING because they get composed ("subnet-" + name); params match on the WHOLE
// value, since a param value is passed through as-is and a substring test on short values ("100")
// would collide with unrelated literals.
//
// Scoped to Intents of the kind this Workflow builds, which is what keeps it quiet: subnet-build's
// `toValue: net-vlan` is an Intent/Vlan name, never compared against a Subnet builder. That literal
// is a real cross-kind topology coupling, but it is placement's to answer, not this check's.
func hardcodedInstanceLiteral(w types.Workflow, in types.Intent) (string, string, bool) {
	values := map[string]string{}
	if params, ok := in.Spec["params"].(map[string]any); ok {
		for k, v := range params {
			if s, ok := v.(string); ok && s != "" {
				values[s] = k
			}
		}
	}
	var hit, why string
	var walk func(v any) bool
	walk = func(v any) bool {
		switch t := v.(type) {
		case string:
			if in.Name != "" && strings.Contains(t, in.Name) {
				hit, why = t, fmt.Sprintf("it names the %s declaration %q, which must arrive as {{.launch.name}}", in.Kind, in.Name)
				return true
			}
			if key, ok := values[t]; ok {
				hit, why = t, fmt.Sprintf("it is %s's declared params.%s, which must arrive as {{.launch.params.%s}}", in.Name, key, key)
				return true
			}
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if walk(t[k]) {
					return true
				}
			}
		case []any:
			for _, e := range t {
				if walk(e) {
					return true
				}
			}
		}
		return false
	}
	for _, st := range w.Steps {
		if walk(map[string]any(st.Params)) {
			return hit, why, true
		}
	}
	return "", "", false
}

// sortedKeys returns a map's keys in deterministic order, so an error message reads the same twice.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// admissionFileYAML is one estate admission policy (ADR-0073 §7.4b): a named set
// of CEL-only admission controls. Reuses controlYAML; only id/when/outcome are
// meaningful (ValidateAdmissionControls rejects run-time typed primitives).
type admissionFileYAML struct {
	Name     string        `yaml:"name"`
	Controls []controlYAML `yaml:"controls"`
}
type admissionDecl struct {
	Name     string
	Controls []types.Control
}

func parseAdmissionFile(path string, raw []byte) (string, admissionDecl, error) {
	var f admissionFileYAML
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", admissionDecl{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	if f.Name == "" {
		return "", admissionDecl{}, fmt.Errorf("desiredstate: %s: admission policy requires a name", path)
	}
	ctrls := make([]types.Control, 0, len(f.Controls))
	for _, c := range f.Controls {
		ctrls = append(ctrls, types.Control{ID: c.ID, When: c.When, Outcome: c.Outcome})
	}
	if err := policy.ValidateAdmissionControls(ctrls); err != nil {
		return "", admissionDecl{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return f.Name, admissionDecl{Name: f.Name, Controls: ctrls}, nil
}

// admissionDirs are the declaration directories admission judges — every estate
// kind except admission/ itself (a policy does not admit itself).
var admissionDirs = []string{
	"views", "credential-refs", "triggers", "workflows", "emitters", "sites",
	"cells", "scim", "notify-sinks", "subscriptions", "baselines", "mcp-servers",
	"intents", "assignments", "blueprints",
}

// admitEstate runs the admission PEP over every declaration manifest through the
// PDP port (ADR-0073 §7.4c). A deny rejects the load with the reasons on the
// record (§1.8). The manifest is admitted as a generic object with a guaranteed
// `kind` (the manifest's own, or the directory's), so admission controls can
// match reliably (e.g. object.kind == 'Workflow').
func admitEstate(root string, controls []types.Control, decider policy.Decider) error {
	for _, sub := range admissionDirs {
		entries, err := os.ReadDir(filepath.Join(root, sub))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("desiredstate: admission read %s: %w", sub, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext != ".yaml" && ext != ".yml" {
				continue
			}
			path := filepath.Join(sub, e.Name())
			raw, err := os.ReadFile(filepath.Join(root, path))
			if err != nil {
				return fmt.Errorf("desiredstate: %s: %w", path, err)
			}
			obj, err := manifestObject(raw, sub)
			if err != nil {
				return fmt.Errorf("desiredstate: admission parse %s: %w", path, err)
			}
			d := decider.Admit(context.Background(), policy.AdmissionRequest{Object: obj, Controls: controls})
			if d.Outcome == types.OutcomeDeny {
				return fmt.Errorf("desiredstate: %s: admission denied — %s", path, admissionReasons(d))
			}
		}
	}
	return nil
}

// manifestObject decodes a declaration into a generic object for admission,
// guaranteeing a `kind` (the manifest's own, else the directory's singular kind).
func manifestObject(raw []byte, sub string) (map[string]any, error) {
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	if _, ok := m["kind"]; !ok {
		m["kind"] = dirKind(sub)
	}
	return m, nil
}

func dirKind(sub string) string {
	switch sub {
	case "views":
		return "View"
	case "workflows":
		return "Workflow"
	case "assignments":
		return "Assignment"
	case "blueprints":
		return "Blueprint"
	case "baselines":
		return "Baseline"
	case "triggers":
		return "Trigger"
	case "emitters":
		return "Emitter"
	case "sites":
		return "Site"
	default:
		return sub
	}
}

// AdmitDeclarations runs the admission PEP (ADR-0073) over already-parsed
// declarations arriving through an imperative door — POST /desired-state/{plan,
// apply}, PUT /views/{name} — rather than the Git reconcile. It closes the
// bypass (enterprise-readiness GOV-2) where admitEstate only guards ParseDir: an
// operator or agent POSTing straight to the API reached the graph with no
// admission. Each declaration is admitted as a {kind, ...} object through the PDP
// port; a deny — or any evaluator error, since the port fails closed — rejects
// the whole request. A nil decider or empty policy is a no-op (no engine
// configured). The object is the typed declaration's JSON shape (the same shape
// the graph holds); an admission control author writes CEL against that.
func AdmitDeclarations(ctx context.Context, decls Declarations, controls []types.Control, decider policy.Decider) error {
	if decider == nil || len(controls) == 0 {
		return nil
	}
	type obj struct {
		kind, name string
		v          any
	}
	var all []obj
	for i := range decls.Views {
		all = append(all, obj{"View", decls.Views[i].Name, decls.Views[i]})
	}
	for i := range decls.Workflows {
		all = append(all, obj{"Workflow", decls.Workflows[i].Name, decls.Workflows[i]})
	}
	for i := range decls.Triggers {
		all = append(all, obj{"Trigger", decls.Triggers[i].Name, decls.Triggers[i]})
	}
	for i := range decls.Emitters {
		all = append(all, obj{"Emitter", decls.Emitters[i].Name, decls.Emitters[i]})
	}
	for i := range decls.Baselines {
		all = append(all, obj{"Baseline", decls.Baselines[i].Name, decls.Baselines[i]})
	}
	for i := range decls.MCPServers {
		all = append(all, obj{"MCPServer", decls.MCPServers[i].Name, decls.MCPServers[i]})
	}
	for i := range decls.Intents {
		all = append(all, obj{"Intent", decls.Intents[i].Name, decls.Intents[i]})
	}
	for i := range decls.Assignments {
		all = append(all, obj{"Assignment", decls.Assignments[i].Name, decls.Assignments[i]})
	}
	for i := range decls.Blueprints {
		all = append(all, obj{"Blueprint", decls.Blueprints[i].Name, decls.Blueprints[i]})
	}
	for i := range decls.Sites {
		all = append(all, obj{"Site", decls.Sites[i].Name, decls.Sites[i]})
	}
	for i := range decls.Cells {
		all = append(all, obj{"Cell", decls.Cells[i].Name, decls.Cells[i]})
	}
	for i := range decls.NotifySinks {
		all = append(all, obj{"Sink", decls.NotifySinks[i].Name, decls.NotifySinks[i]})
	}
	for i := range decls.Subscriptions {
		all = append(all, obj{"Subscription", decls.Subscriptions[i].Name, decls.Subscriptions[i]})
	}
	for _, o := range all {
		m, err := declarationObject(o.kind, o.v)
		if err != nil {
			return fmt.Errorf("desiredstate: admission encode %s/%s: %w", o.kind, o.name, err)
		}
		d := decider.Admit(ctx, policy.AdmissionRequest{Object: m, Controls: controls})
		if d.Outcome == types.OutcomeDeny {
			return fmt.Errorf("desiredstate: %s/%s: admission denied — %s", o.kind, o.name, admissionReasons(d))
		}
	}
	return nil
}

// declarationObject encodes a typed declaration to the generic admission object,
// guaranteeing a `kind` — the declaration's own (e.g. an Intent's Certificate/
// Application sub-kind) when present, else the declared fallback. Mirrors
// manifestObject so the imperative door and the Git door admit the same shape.
func declarationObject(fallbackKind string, v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	if k, _ := m["kind"].(string); k == "" {
		m["kind"] = fallbackKind
	}
	return m, nil
}

func admissionReasons(d types.Decision) string {
	parts := make([]string, 0, len(d.Reasons))
	for _, r := range d.Reasons {
		if r.ControlID != "" {
			parts = append(parts, r.ControlID+": "+r.Message)
		} else {
			parts = append(parts, r.Message)
		}
	}
	return strings.Join(parts, "; ")
}

// parseKind reads one declaration directory; optional dirs may be absent.
func parseKind[T any](dir string, optional bool, parse func(path string, raw []byte) (string, T, error)) ([]T, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) && optional {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("desiredstate: read declarations: %w", err)
	}
	seen := map[string]string{} // declared name → file
	var out []T
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("desiredstate: %s: %w", path, err)
		}
		name, decl, err := parse(path, raw)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[name]; dup {
			return nil, fmt.Errorf("desiredstate: %q declared in both %s and %s", name, prev, path)
		}
		seen[name] = path
		out = append(out, decl)
	}
	return out, nil
}

func parseViewFile(path string, raw []byte) (string, Declaration, error) {
	var f declFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // typos in declarations must fail, not vanish
	if err := dec.Decode(&f); err != nil {
		return "", Declaration{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	if f.Name == "" {
		return "", Declaration{}, fmt.Errorf("desiredstate: %s: name is required", path)
	}
	sel, err := f.Selector.toSelector()
	if err != nil {
		return "", Declaration{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	// A View selector may bind only the param namespace ({{.param.x}}) — a
	// parametrized View (ADR-0024). event/spec are not available here; catch
	// them at declaration, not at launch.
	if err := checkTemplateNamespaces("view "+f.Name, map[string]bool{"param": true}, selectorStrings(sel)...); err != nil {
		return "", Declaration{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return f.Name, Declaration{Name: f.Name, Selector: sel}, nil
}

// selectorStrings returns a selector's templatable string values (label
// values, facet equals) for namespace-scope checking.
func selectorStrings(sel types.ViewSelector) []any {
	var out []any
	for _, v := range sel.Labels {
		out = append(out, v)
	}
	for _, f := range sel.Facets {
		out = append(out, string(f.Equals))
	}
	return out
}

// credRefFile is the credential-refs/*.yaml shape (pointer + injection
// policy only — nothing in the declaration can hold material, §2.5).
type credRefFile struct {
	Name      string            `yaml:"name"`
	OwnerTeam string            `yaml:"ownerTeam"`
	Backend   string            `yaml:"backend"`
	Locator   map[string]any    `yaml:"locator"`
	Injection []credRefInjxYAML `yaml:"injection"`
	// GateOnly asserts the ref brokers NO material — it is purely an Action's authz
	// gate (ADR-0052/0092). It makes an injection-less ref DELIBERATE, so an author
	// who merely dropped the injection block still fails compile (§1.8), and a
	// reviewer sees the intent in Git rather than inferring it from an absent block.
	GateOnly bool `yaml:"gateOnly"`
}
type credRefInjxYAML struct {
	Key  string `yaml:"key"`
	As   string `yaml:"as"`
	Name string `yaml:"name"`
}

func parseCredentialRefFile(path string, raw []byte) (string, types.CredentialRef, error) {
	var f credRefFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.CredentialRef{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	ref, err := f.toCredentialRef()
	if err != nil {
		return "", types.CredentialRef{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return ref.Name, ref, nil
}

func (f credRefFile) toCredentialRef() (types.CredentialRef, error) {
	var ref types.CredentialRef
	if f.Name == "" || f.OwnerTeam == "" {
		return ref, fmt.Errorf("credential ref requires name and ownerTeam")
	}
	switch f.Backend {
	case types.BackendK8sSecret, types.BackendVault, types.BackendWorkloadIdentity:
	default:
		return ref, fmt.Errorf("credential ref %s: unknown backend %q", f.Name, f.Backend)
	}
	if len(f.Locator) == 0 {
		return ref, fmt.Errorf("credential ref %s: locator is required", f.Name)
	}
	locator, err := json.Marshal(f.Locator)
	if err != nil {
		return ref, err
	}
	// Injection drives ALL material projection — pod secretKeyRef/volumes AND the
	// plugin SecretBroker's authorized keys — so empty injection ⇒ nothing resolved.
	// That is legal ONLY as a DELIBERATE gate-only ref (gateOnly: true, ADR-0052 §2.5
	// / ADR-0092 — an Action's cred is its authz gate even when the plugin acts via
	// its own pod ServiceAccount). Requiring the marker keeps an ACCIDENTALLY-dropped
	// injection block failing at compile (§1.8), not silently degrading to "resolves
	// nothing". A gateOnly ref with injection entries is contradictory — reject it.
	switch {
	case len(f.Injection) == 0 && !f.GateOnly:
		return ref, fmt.Errorf("credential ref %s: injection policy is required (or set gateOnly: true for a material-less authz-gate ref — ADR-0052/0092)", f.Name)
	case len(f.Injection) > 0 && f.GateOnly:
		return ref, fmt.Errorf("credential ref %s: gateOnly refs broker no material — remove either the injection block or gateOnly", f.Name)
	}
	inj := make([]types.CredentialInjection, len(f.Injection))
	for i, x := range f.Injection {
		if x.Key == "" || x.Name == "" {
			return ref, fmt.Errorf("credential ref %s: injection %d requires key and name", f.Name, i)
		}
		if x.As != types.InjectEnv && x.As != types.InjectFile {
			return ref, fmt.Errorf("credential ref %s: injection %d: as must be env or file", f.Name, i)
		}
		inj[i] = types.CredentialInjection{Key: x.Key, As: x.As, Name: x.Name}
	}
	return types.CredentialRef{
		Name: f.Name, OwnerTeam: f.OwnerTeam, Backend: f.Backend,
		Locator: locator, Injection: inj, GateOnly: f.GateOnly,
	}, nil
}

// triggerFile is the triggers/*.yaml shape (ADR-0010). The declaration is
// also an impersonation grant — principal names the service identity the
// fired Runs execute as — which is exactly why Triggers are CaC-only: Git
// review authorizes the binding.
type triggerFile struct {
	Name            string         `yaml:"name"`
	Kind            string         `yaml:"kind"`
	Cron            string         `yaml:"cron"`
	Paused          bool           `yaml:"paused"`
	Emitter         string         `yaml:"emitter"`
	When            string         `yaml:"when"`
	CooldownSeconds int            `yaml:"cooldownSeconds"`
	ViewName        string         `yaml:"viewName"`
	ViewParams      map[string]any `yaml:"viewParams"`
	Actuator        string         `yaml:"actuator"`
	Params          map[string]any `yaml:"params"`
	Inputs          map[string]any `yaml:"inputs"`
	Slices          int            `yaml:"slices"`
	CredentialRefs  []string       `yaml:"credentialRefs"`
	Principal       string         `yaml:"principal"`
	WorkflowName    string         `yaml:"workflowName"`
	FacetWriteScope []string       `yaml:"facetWriteScope"`
	Environments    []string       `yaml:"environments"`
}

func parseTriggerFile(path string, raw []byte, opts ...ValidateOption) (string, types.Trigger, error) {
	var f triggerFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.Trigger{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	if f.Kind == "" {
		f.Kind = types.TriggerSchedule
	}
	t := types.Trigger{
		Name: f.Name, Kind: f.Kind, Cron: f.Cron, Paused: f.Paused,
		Emitter: f.Emitter, When: f.When, CooldownSeconds: f.CooldownSeconds,
		ViewName: f.ViewName, ViewParams: f.ViewParams,
		Actuator: f.Actuator, Params: f.Params,
		Slices: f.Slices, CredentialRefs: f.CredentialRefs, Principal: f.Principal,
		WorkflowName: f.WorkflowName, Inputs: f.Inputs, FacetWriteScope: f.FacetWriteScope,
		Environments: f.Environments,
	}
	if err := ValidateTrigger(t, opts...); err != nil {
		return "", types.Trigger{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return t.Name, t, nil
}

// ValidateTrigger checks one Trigger declaration; exported because the API's
// desired-state plan/apply path (the CLI applying the same Git checkout)
// validates the identical document shape.
func ValidateTrigger(t types.Trigger, opts ...ValidateOption) error {
	if t.Name == "" {
		return fmt.Errorf("trigger requires a name")
	}
	// Launch target: a single Run (viewName) XOR a declared Workflow.
	runLaunch := t.ViewName != ""
	workflowLaunch := t.WorkflowName != ""
	if runLaunch == workflowLaunch {
		return fmt.Errorf("trigger %s: exactly one launch target — viewName or workflowName", t.Name)
	}
	if workflowLaunch && (t.Actuator != "" || t.Params != nil || t.Slices != 0 || len(t.CredentialRefs) > 0) {
		return fmt.Errorf("trigger %s: workflowName launches carry no Step fields (the Workflow declares its own) — "+
			"to parameterize the Workflow itself, use `inputs` (ADR-0118 D5)", t.Name)
	}
	// The mirror of the rule above: a Run target has no launch interface to fill, so `inputs`
	// there would be accepted and read by nothing — the half-declaration shape this codebase
	// keeps finding (ADR-0117 D5a's port with no address).
	if runLaunch && t.Inputs != nil {
		return fmt.Errorf("trigger %s: inputs are the launch interface of a workflowName target; "+
			"a viewName Run has none (its Step params are `params`)", t.Name)
	}
	// Template namespace scope (ADR-0024): a Trigger's params/viewParams may
	// bind {{.event.x}} only on event-kind Triggers (a schedule fire has no
	// event); the spec/param namespaces belong to the compiler and the View,
	// not here.
	allowed := map[string]bool{}
	if t.Kind == types.TriggerEvent {
		allowed["event"] = true
	}
	if err := checkTemplateNamespaces("trigger "+t.Name, allowed, t.Params, t.ViewParams, t.Inputs); err != nil {
		return err
	}
	switch t.Kind {
	case types.TriggerSchedule:
		if t.Cron == "" {
			return fmt.Errorf("trigger %s: schedule kind requires cron", t.Name)
		}
		if t.Emitter != "" || t.When != "" {
			return fmt.Errorf("trigger %s: emitter/when belong to kind event", t.Name)
		}
	case types.TriggerEvent:
		if t.Emitter == "" || t.When == "" {
			return fmt.Errorf("trigger %s: event kind requires emitter and when", t.Name)
		}
		if t.Cron != "" || t.Paused {
			return fmt.Errorf("trigger %s: cron/paused belong to kind schedule", t.Name)
		}
		// CEL compiles at declaration parse — a bad rule fails its file,
		// never silently at event time (§1.8; ADR-0018).
		if _, err := rules.Compile(t.When); err != nil {
			return fmt.Errorf("trigger %s: %w", t.Name, err)
		}
	default:
		return fmt.Errorf("trigger %s: unknown kind %q (schedule, event)", t.Name, t.Kind)
	}
	if t.Slices < 0 {
		return fmt.Errorf("trigger %s: slices must be >= 0", t.Name)
	}
	// CredentialRefs without a Principal can never resolve at dispatch
	// (§2.5: use is checked against the launching identity) — fail the
	// declaration, not the Run.
	if len(t.CredentialRefs) > 0 && t.Principal == "" {
		return fmt.Errorf("trigger %s: credentialRefs require a principal", t.Name)
	}
	if runLaunch {
		if err := validateParamsContract(t.Actuator, t.Params, opts...); err != nil {
			return fmt.Errorf("trigger %s: %w", t.Name, err)
		}
	}
	return nil
}

// emitterFile is the emitters/*.yaml shape (ADR-0018). tokenHash is
// hex(sha256(token)) — the declaration never holds the token itself (§2.5).
type emitterFile struct {
	Name      string `yaml:"name"`
	Kind      string `yaml:"kind"`
	TokenHash string `yaml:"tokenHash"`
}

func parseEmitterFile(path string, raw []byte) (string, types.Emitter, error) {
	var f emitterFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.Emitter{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	e := types.Emitter{Name: f.Name, Kind: f.Kind, TokenHash: strings.ToLower(f.TokenHash)}
	if err := ValidateEmitter(e); err != nil {
		return "", types.Emitter{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return e.Name, e, nil
}

// ValidateEmitter checks one Emitter declaration.
func ValidateEmitter(e types.Emitter) error {
	if e.Name == "" {
		return fmt.Errorf("emitter requires a name")
	}
	switch e.Kind {
	case types.EmitterWebhook, types.EmitterAlertmanager:
		// Receive kinds authenticate an inbound POST by token.
		if len(e.TokenHash) != 64 {
			return fmt.Errorf("emitter %s: tokenHash must be hex(sha256(token)) — 64 hex chars", e.Name)
		}
		if _, err := hex.DecodeString(e.TokenHash); err != nil {
			return fmt.Errorf("emitter %s: tokenHash is not hex", e.Name)
		}
	case types.EmitterStream:
		// A stream subscriber outbound-connects; it has no inbound token.
		if e.TokenHash != "" {
			return fmt.Errorf("emitter %s: a stream emitter is outbound-subscribed and must not carry a tokenHash", e.Name)
		}
	default:
		return fmt.Errorf("emitter %s: unknown kind %q (webhook, alertmanager, stream)", e.Name, e.Kind)
	}
	return nil
}

// scimFile is the scim/*.yaml shape (ADR-0035): a registered SCIM IdP. It holds
// the sha256 of the IdP's bearer token (§2.5 — material never stored) and the
// group→team mappings. A mapped team's MEMBERSHIP becomes IdP-owned; the
// reconcile one-owner guard forbids CaC also declaring its members (§2.1).
type scimFile struct {
	Name          string `yaml:"name"`
	TokenHash     string `yaml:"tokenHash"`
	GroupMappings []struct {
		Group string `yaml:"group"`
		Team  string `yaml:"team"`
	} `yaml:"groupMappings"`
}

func parseScimFile(path string, raw []byte) (string, types.SCIMIdP, error) {
	var f scimFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.SCIMIdP{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	d := types.SCIMIdP{Name: f.Name, TokenHash: strings.ToLower(f.TokenHash)}
	for _, m := range f.GroupMappings {
		d.GroupMappings = append(d.GroupMappings, types.GroupMapping{Group: m.Group, Team: m.Team})
	}
	if err := ValidateScim(d); err != nil {
		return "", types.SCIMIdP{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return d.Name, d, nil
}

// ValidateScim checks one SCIM IdP declaration.
func ValidateScim(d types.SCIMIdP) error {
	if d.Name == "" {
		return fmt.Errorf("scim idp requires a name")
	}
	if len(d.TokenHash) != 64 {
		return fmt.Errorf("scim idp %s: tokenHash must be hex(sha256(token)) — 64 hex chars", d.Name)
	}
	if _, err := hex.DecodeString(d.TokenHash); err != nil {
		return fmt.Errorf("scim idp %s: tokenHash is not hex", d.Name)
	}
	seen := map[string]bool{}
	for _, m := range d.GroupMappings {
		if m.Group == "" || m.Team == "" {
			return fmt.Errorf("scim idp %s: groupMapping requires both group and team", d.Name)
		}
		if seen[m.Group] {
			return fmt.Errorf("scim idp %s: duplicate mapping for group %q", d.Name, m.Group)
		}
		seen[m.Group] = true
	}
	return nil
}

// siteFile is the sites/*.yaml shape (ADR-0032). A Site declaration holds NO
// secret material — the agent resolves credential pointers against its OWN
// local Secrets at spawn (§2.5).
type siteFile struct {
	Name        string `yaml:"name"`
	Mode        string `yaml:"mode"`
	Namespace   string `yaml:"namespace"`
	Description string `yaml:"description"`
	// Cell is the control-plane Cell this Site belongs to (ADR-0044:
	// Site → Cell → region). Empty ⇒ the built-in LocalCell.
	Cell string `yaml:"cell"`
}

func parseSiteFile(path string, raw []byte) (string, types.Site, error) {
	var f siteFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.Site{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	s := types.Site{Name: f.Name, Mode: f.Mode, Namespace: f.Namespace, Description: f.Description, Cell: f.Cell, DeclaredBy: "cac"}
	if err := ValidateSite(s); err != nil {
		return "", types.Site{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return s.Name, s, nil
}

// ValidateSite checks one Site declaration.
func ValidateSite(s types.Site) error {
	if s.Name == "" {
		return fmt.Errorf("site requires a name")
	}
	if s.Name == types.LocalSite {
		return fmt.Errorf("site name %q is reserved for the built-in central locus", types.LocalSite)
	}
	if s.Mode != types.SiteModePush && s.Mode != types.SiteModePull {
		return fmt.Errorf("site %s: unknown mode %q (push, pull)", s.Name, s.Mode)
	}
	// A Site name is a NATS subject token (stratt.dispatch.<name>, ADR-0032), so
	// it must not contain the dot/wildcard/space characters that would split or
	// widen the subject.
	if strings.ContainsAny(s.Name, ". \t*>") {
		return fmt.Errorf("site %s: name must not contain '.', whitespace, or NATS wildcards ('*','>')", s.Name)
	}
	return nil
}

// cellFile is the cells/*.yaml shape (ADR-0044). A Cell declaration is the
// federation router's peer registry: name + region + the peer's strattd API
// endpoint. No secret material.
type cellFile struct {
	Name           string `yaml:"name"`
	Region         string `yaml:"region"`
	Endpoint       string `yaml:"endpoint"`
	DispatchPrefix string `yaml:"dispatchPrefix"`
	Description    string `yaml:"description"`
	AuthzHome      bool   `yaml:"authzHome"`
}

func parseCellFile(path string, raw []byte) (string, types.Cell, error) {
	var f cellFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.Cell{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	c := types.Cell{Name: f.Name, Region: f.Region, Endpoint: f.Endpoint, DispatchPrefix: f.DispatchPrefix, Description: f.Description, AuthzHome: f.AuthzHome, DeclaredBy: "cac"}
	if err := ValidateCell(c); err != nil {
		return "", types.Cell{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return c.Name, c, nil
}

// ValidateCell checks one Cell declaration.
func ValidateCell(c types.Cell) error {
	if c.Name == "" {
		return fmt.Errorf("cell requires a name")
	}
	if c.Name == types.LocalCell {
		return fmt.Errorf("cell name %q is reserved for the built-in single-Cell default", types.LocalCell)
	}
	// The Cell name and any DispatchPrefix become NATS subject tokens + JetStream
	// stream names (ADR-0044 slice 6) and the Temporal namespace/queue + leader
	// lease (slice 1); a '.' or wildcard would silently reshape the NATS topology.
	// Reject them at compile — the highest, earliest gate.
	if !types.ValidCellScopeToken(c.Name) {
		return fmt.Errorf("cell %s: name must be NATS-safe (lower-case alphanumeric + '-', no '.'/'*'/'>')", c.Name)
	}
	if !types.ValidCellScopeToken(c.DispatchPrefix) {
		return fmt.Errorf("cell %s: dispatchPrefix %q must be NATS-safe (lower-case alphanumeric + '-', no '.'/'*'/'>')", c.Name, c.DispatchPrefix)
	}
	if c.Region == "" {
		return fmt.Errorf("cell %s: region is required", c.Name)
	}
	// endpoint is the peer's strattd API address the federation router dials.
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("cell %s: endpoint must be an absolute URL (got %q)", c.Name, c.Endpoint)
	}
	return nil
}

// validateCellSet enforces the exactly-one authz-home invariant across a named
// fleet (ADR-0044 slice 4): the authz-home Cell's leader is the SOLE writer of
// the shared OpenFGA tuple store, so two would thrash and zero would let grants
// go stale. A pure single-Cell estate (no declared Cells) is fine — the built-in
// 'local' Cell is the trivial authz writer.
func validateCellSet(cells []types.Cell) error {
	if len(cells) == 0 {
		return nil
	}
	var homes []string
	for _, c := range cells {
		if c.AuthzHome {
			homes = append(homes, c.Name)
		}
	}
	switch len(homes) {
	case 1:
		return nil
	case 0:
		return fmt.Errorf("exactly one Cell must set authzHome: true (the sole OpenFGA tuple writer); none of %d declared Cells does", len(cells))
	default:
		return fmt.Errorf("exactly one Cell may set authzHome: true; %d do: %s", len(homes), strings.Join(homes, ", "))
	}
}

// notifySinkFile is the notify-sinks/*.yaml shape (ADR-0027). No secret
// material: the delivery url/token come from the bound CredentialRef, injected
// into the delivery pod at spawn (§2.5).
type notifySinkFile struct {
	Name          string `yaml:"name"`
	Kind          string `yaml:"kind"`
	Principal     string `yaml:"principal"`
	CredentialRef string `yaml:"credentialRef"`
	Config        struct {
		BodyTemplate string `yaml:"bodyTemplate"`
		// Params is the driver's own bag (ADR-0125 D2) — deliberately NOT
		// KnownFields-checked here, because core does not know the driver's
		// fields. The driver's pinned input Contract is what refuses an unknown
		// one, at delivery, by name (§1.5).
		Params   map[string]any `yaml:"params"`
		Endpoint string         `yaml:"endpoint"`
		Index    string         `yaml:"index"`
		Facility int            `yaml:"facility"`
		Insecure bool           `yaml:"insecure"`
	} `yaml:"config"`
}

func parseNotifySinkFile(path string, raw []byte) (string, types.Sink, error) {
	var f notifySinkFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.Sink{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	s := types.Sink{Name: f.Name, Kind: f.Kind, Principal: f.Principal, CredentialRef: f.CredentialRef,
		Config: types.SinkConfig{
			BodyTemplate: f.Config.BodyTemplate, Params: f.Config.Params,
			Endpoint: f.Config.Endpoint, Index: f.Config.Index,
			Facility: f.Config.Facility, Insecure: f.Config.Insecure,
		}}
	if err := ValidateNotifySink(s); err != nil {
		return "", types.Sink{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return s.Name, s, nil
}

// ValidateNotifySink checks one Sink declaration. Exported for API reuse.
func ValidateNotifySink(s types.Sink) error {
	if s.Name == "" {
		return fmt.Errorf("notify sink requires a name")
	}
	// SIEM audit-egress sinks (ADR-0034): declared in Git like any Sink, shipped
	// to by the stratt-forwarder. Require an endpoint; the credential (HEC token,
	// TLS) is a CredentialRef injected into the forwarder pod (§2.5), optional
	// for a plain-TCP dev syslog. When present it is authz-checked like webhook.
	if types.SIEMSinkKinds[s.Kind] {
		if s.Config.Endpoint == "" {
			return fmt.Errorf("sink %s: %s requires config.endpoint", s.Name, s.Kind)
		}
		if s.CredentialRef != "" && s.Principal == "" {
			return fmt.Errorf("sink %s: principal is required when a credentialRef is set (the delivery credential check, §2.5/§1.6)", s.Name)
		}
		return nil
	}
	// Everything else is a NOTIFY sink, and core deliberately does not hold a list
	// of the kinds it may be (ADR-0125 D1). The kind names a delivery Action; a
	// kind no plugin provides is an unresolvable Action at delivery, reported by
	// name on the notify_delivery surface. A closed set here would mean a
	// third-party driver could not ship without a core release — the exact
	// content-blindness §1.4 buys, and the reason the webhook switch is gone.
	if s.Kind == "" {
		return fmt.Errorf("sink %s: kind is required (it names the delivery Action, e.g. webhook → notify/webhook)", s.Name)
	}
	if s.CredentialRef == "" {
		return fmt.Errorf("notify sink %s: credentialRef is required (the delivery url/token are injected from it, never inline — §2.5)", s.Name)
	}
	if s.Principal == "" {
		return fmt.Errorf("notify sink %s: principal is required (it must hold `use` on the credentialRef — the delivery credential check, §2.5/§1.6)", s.Name)
	}
	return nil
}

// subscriptionFile is the subscriptions/*.yaml shape (ADR-0027).
type subscriptionFile struct {
	Name            string   `yaml:"name"`
	On              []string `yaml:"on"`
	Match           string   `yaml:"match"`
	Sink            string   `yaml:"sink"`
	CooldownSeconds int      `yaml:"cooldownSeconds"`
}

func parseSubscriptionFile(path string, raw []byte) (string, types.Subscription, error) {
	var f subscriptionFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.Subscription{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	sub := types.Subscription{
		Name: f.Name, On: f.On, Match: f.Match, Sink: f.Sink, CooldownSeconds: f.CooldownSeconds,
	}
	if err := ValidateSubscription(sub); err != nil {
		return "", types.Subscription{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return sub.Name, sub, nil
}

// noticeKinds is the closed set of notice kinds a Subscription may listen for
// (ADR-0027) — an unknown kind is a declaration error, never a silent no-op.
var noticeKinds = map[string]bool{
	types.NoticeRunFailed:   true,
	types.NoticeRunCanceled: true,
	types.NoticeFindingOpen: true,
	types.NoticeGatePending: true,
}

// ValidateSubscription checks one Subscription declaration, including that its
// CEL match compiles (fail the file, never silently at notice time — §1.8).
func ValidateSubscription(sub types.Subscription) error {
	if sub.Name == "" {
		return fmt.Errorf("subscription requires a name")
	}
	if sub.Sink == "" {
		return fmt.Errorf("subscription %s: sink is required", sub.Name)
	}
	if len(sub.On) == 0 {
		return fmt.Errorf("subscription %s: on must list at least one notice kind", sub.Name)
	}
	for _, k := range sub.On {
		if !noticeKinds[k] {
			return fmt.Errorf("subscription %s: unknown notice kind %q (run.failed, run.canceled, finding.open, gate.pending)", sub.Name, k)
		}
	}
	if sub.CooldownSeconds < 0 {
		return fmt.Errorf("subscription %s: cooldownSeconds must be >= 0", sub.Name)
	}
	if sub.Match != "" {
		if _, err := rules.Compile(sub.Match); err != nil {
			return fmt.Errorf("subscription %s: match: %w", sub.Name, err)
		}
	}
	return nil
}

// baselineFile is the baselines/*.yaml shape (ADR-0019): a check Step +
// cadence + remediation ref. Like Triggers, the declaration is an
// impersonation grant (principal) — Git review authorizes it; CaC-only.
type baselineFile struct {
	Name                string         `yaml:"name"`
	ViewName            string         `yaml:"viewName"`
	Actuator            string         `yaml:"actuator"`
	Params              map[string]any `yaml:"params"`
	Slices              int            `yaml:"slices"`
	CredentialRefs      []string       `yaml:"credentialRefs"`
	Principal           string         `yaml:"principal"`
	Cron                string         `yaml:"cron"`
	Paused              bool           `yaml:"paused"`
	Severity            string         `yaml:"severity"`
	DampingObservations int            `yaml:"dampingObservations"`
	RemediationWorkflow string         `yaml:"remediationWorkflow"`
	RemediationParams   map[string]any `yaml:"remediationParams"`
	Framework           string         `yaml:"framework"`
	Environments        []string       `yaml:"environments"`
	// FacetWriteScope is the Facet namespaces this Baseline's actuation may write
	// back (ADR-0054): the effective allowlist is the actuator's grant ∩ this scope.
	// Empty admits no facet write-back (tight default). Moot for the observation
	// Mode below, which reads and never writes.
	FacetWriteScope []string `yaml:"facetWriteScope"`

	// facet-observation variant (ADR-0033): a hand-written Baseline that
	// asserts expected Facet values graph-side (no check Step, no actuator).
	// The desired state is "the Entities in viewName should carry these Facet
	// values" (§2.4); the collector projects the Facets separately (§1.2).
	// There is deliberately no `claim` field: an observation reads, it never
	// writes/owns the Facet, so there is nothing to claim (the anti-GPO claim
	// concept is the compiler's, over Assignment-owned writes — ADR-0023/§2.4).
	Mode     string                 `yaml:"mode"`
	Expected []facetExpectationFile `yaml:"expected"`
	// RequiredRelations are outgoing relation types each targeted Entity must carry
	// (ADR-0085) — the topology sibling of Expected. Presence-only, tool-blind.
	RequiredRelations []string `yaml:"requiredRelations"`
}

// facetExpectationFile is the yaml shape of one facet-observation expectation.
// It mirrors types.FacetExpectation but carries explicit yaml tags (the type's
// json tags don't govern yaml decoding) so `notBefore` decodes as written.
type facetExpectationFile struct {
	Namespace string `yaml:"namespace"`
	Path      string `yaml:"path"`
	Equals    any    `yaml:"equals"`
	Contains  any    `yaml:"contains"`
	NotBefore string `yaml:"notBefore"`
}

// toExpectation converts a yaml expectation into the typed form, JSON-encoding
// the equals/contains value. Returns an error only if the value is unencodable.
func (e facetExpectationFile) toExpectation() (types.FacetExpectation, error) {
	exp := types.FacetExpectation{Namespace: e.Namespace, Path: e.Path, NotBefore: e.NotBefore}
	if e.Equals != nil {
		raw, err := json.Marshal(e.Equals)
		if err != nil {
			return types.FacetExpectation{}, err
		}
		exp.Equals = raw
	}
	if e.Contains != nil {
		raw, err := json.Marshal(e.Contains)
		if err != nil {
			return types.FacetExpectation{}, err
		}
		exp.Contains = raw
	}
	return exp, nil
}

func parseBaselineFile(path string, raw []byte, opts ...ValidateOption) (string, types.Baseline, error) {
	var f baselineFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.Baseline{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	b := types.Baseline{
		Name: f.Name, ViewName: f.ViewName, Actuator: f.Actuator, Params: f.Params,
		Slices: f.Slices, CredentialRefs: f.CredentialRefs, Principal: f.Principal,
		Cron: f.Cron, Paused: f.Paused, Severity: f.Severity,
		DampingObservations: f.DampingObservations,
		RemediationWorkflow: f.RemediationWorkflow, RemediationParams: f.RemediationParams,
		Framework: f.Framework,
		Mode:      f.Mode, FacetWriteScope: f.FacetWriteScope, Environments: f.Environments,
		RequiredRelations: f.RequiredRelations,
	}
	for _, ef := range f.Expected {
		exp, err := ef.toExpectation()
		if err != nil {
			return "", types.Baseline{}, fmt.Errorf("desiredstate: %s: expected: %w", path, err)
		}
		b.Expected = append(b.Expected, exp)
	}
	if err := ValidateBaseline(b, opts...); err != nil {
		return "", types.Baseline{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return b.Name, b, nil
}

// ValidateBaseline checks one Baseline declaration (ADR-0019). The check
// must be read-only by construction: only Actuators with check semantics are
// accepted, opentofu is pinned to plan mode, and ansible's check flag is the
// platform's to set — a declaration cannot even ask for a mutating check.
func ValidateBaseline(b types.Baseline, opts ...ValidateOption) error {
	if b.Name == "" {
		return fmt.Errorf("baseline requires a name")
	}
	if b.ViewName == "" {
		return fmt.Errorf("baseline %s: viewName is required", b.Name)
	}
	if b.Cron == "" {
		return fmt.Errorf("baseline %s: cron is required (the check cadence)", b.Name)
	}
	switch b.Severity {
	case types.SeverityInfo, types.SeverityWarning, types.SeverityCritical:
	default:
		return fmt.Errorf("baseline %s: severity must be info, warning, or critical", b.Name)
	}
	if b.DampingObservations < 0 {
		return fmt.Errorf("baseline %s: dampingObservations must be >= 0", b.Name)
	}
	if b.Slices < 0 {
		return fmt.Errorf("baseline %s: slices must be >= 0", b.Name)
	}

	// facet-observation Baselines (ADR-0033) assert expected Facet values
	// graph-side — they have no check Step, so the actuator/params/read-only
	// checks below do not apply. The desired state is data; the collector
	// projects the Facets separately (§1.2). viewName + cron are validated
	// above; here we require well-formed expectations and no execution fields.
	if b.Mode == types.FacetObservation {
		if b.Actuator != "" || len(b.Params) > 0 {
			return fmt.Errorf("baseline %s: facet-observation baselines take no actuator/params (the check is graph-side)", b.Name)
		}
		if len(b.CredentialRefs) > 0 {
			return fmt.Errorf("baseline %s: facet-observation baselines take no credentialRefs", b.Name)
		}
		// A facet-observation Baseline asserts per-node facet values (Expected)
		// and/or graph topology (RequiredRelations, ADR-0085) — at least one
		// expectation of either kind is required (an empty check is a no-op bug).
		if len(b.Expected) == 0 && len(b.RequiredRelations) == 0 {
			return fmt.Errorf("baseline %s: facet-observation requires at least one expected value or required relation", b.Name)
		}
		for i, rel := range b.RequiredRelations {
			if rel == "" {
				return fmt.Errorf("baseline %s: requiredRelations[%d]: relation type must not be empty", b.Name, i)
			}
		}
		for i, exp := range b.Expected {
			if exp.Namespace == "" {
				return fmt.Errorf("baseline %s: expected[%d]: namespace is required", b.Name, i)
			}
			set := 0
			if len(exp.Equals) > 0 {
				set++
			}
			if len(exp.Contains) > 0 {
				set++
			}
			if exp.NotBefore != "" {
				set++
			}
			if set != 1 {
				return fmt.Errorf("baseline %s: expected[%d]: exactly one of equals, contains, or notBefore is required", b.Name, i)
			}
		}
		return nil
	}
	if b.Mode != "" {
		return fmt.Errorf("baseline %s: unknown mode %q (only %q, or empty for a check Step)", b.Name, b.Mode, types.FacetObservation)
	}

	// A baseline is read-only by platform INVARIANT — the launch path forces the
	// DryRun bit and rejects any Actuator that can't honor it (its reconciled
	// DryRunnable capability). Validation stays CONTENT-BLIND (ADR-0046): it does not
	// switch on tool name nor police tool-specific params (e.g. an inert params.check
	// the read-only shim ignores) — the seam is the Contract, not a tool roster.
	if len(b.CredentialRefs) > 0 && b.Principal == "" {
		return fmt.Errorf("baseline %s: credentialRefs require a principal", b.Name)
	}
	if err := validateParamsContract(b.Actuator, b.Params, opts...); err != nil {
		return fmt.Errorf("baseline %s: %w", b.Name, err)
	}
	return nil
}

// mcpServerFile is the mcp-servers/*.yaml shape (ADR-0022). For stdio the
// declaration carries the server's entire source — the sandbox runs exactly
// what Git review approved, never a command derived from Run-time input
// (the structural stdio-injection mitigation; dependency-scout mandate).
type mcpServerFile struct {
	Name      string `yaml:"name"`
	Transport string `yaml:"transport"`
	Rev       int    `yaml:"rev"`
	Script    string `yaml:"script"`
	Endpoint  string `yaml:"endpoint"`
	TokenRef  *struct {
		CredentialRef string `yaml:"credentialRef"`
		Key           string `yaml:"key"`
	} `yaml:"tokenRef"`
}

func parseMCPServerFile(path string, raw []byte) (string, types.MCPServer, error) {
	var f mcpServerFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.MCPServer{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	m := types.MCPServer{
		Name: f.Name, Transport: f.Transport, Rev: f.Rev,
		Script: f.Script, Endpoint: f.Endpoint,
	}
	if f.TokenRef != nil {
		m.TokenRef = &types.MCPTokenRef{CredentialRef: f.TokenRef.CredentialRef, Key: f.TokenRef.Key}
	}
	if err := ValidateMCPServer(m); err != nil {
		return "", types.MCPServer{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return m.Name, m, nil
}

// ValidateMCPServer checks one MCPServer declaration (ADR-0022).
func ValidateMCPServer(m types.MCPServer) error {
	if m.Name == "" {
		return fmt.Errorf("mcp server requires a name")
	}
	if m.Rev < 1 {
		return fmt.Errorf("mcp server %s: rev must be >= 1 (it keys the pinned tool contracts)", m.Name)
	}
	switch m.Transport {
	case types.MCPTransportStdio:
		if m.Script == "" {
			return fmt.Errorf("mcp server %s: stdio transport requires script (the server source, Git-reviewed)", m.Name)
		}
		if m.Endpoint != "" || m.TokenRef != nil {
			return fmt.Errorf("mcp server %s: endpoint/tokenRef belong to http transport", m.Name)
		}
	case types.MCPTransportHTTP:
		if m.Endpoint == "" {
			return fmt.Errorf("mcp server %s: http transport requires endpoint", m.Name)
		}
		if m.Script != "" {
			return fmt.Errorf("mcp server %s: script belongs to stdio transport", m.Name)
		}
		if m.TokenRef != nil && (m.TokenRef.CredentialRef == "" || m.TokenRef.Key == "") {
			return fmt.Errorf("mcp server %s: tokenRef requires credentialRef and key", m.Name)
		}
	default:
		return fmt.Errorf("mcp server %s: unknown transport %q (stdio, http)", m.Name, m.Transport)
	}
	return nil
}

// ── Intent layer (ADR-0023): Intent / Assignment / Blueprint ────────────────

// intentFile is the intents/*.yaml shape.
type intentFile struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"`
	// Version is the configuration version (ADR-0119 D1); absent means 1.
	Version  int            `yaml:"version"`
	Spec     map[string]any `yaml:"spec"`
	OnRemove string         `yaml:"onRemove"`
}

func parseIntentFile(path string, raw []byte) (string, types.Intent, error) {
	var f intentFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.Intent{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	in := types.Intent{Name: f.Name, Kind: f.Kind, Version: f.Version, Spec: f.Spec, OnRemove: f.OnRemove}
	if err := ValidateIntent(in); err != nil {
		return "", types.Intent{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	// Dedup key is name@version, symmetric with parseBlueprintFile — two versions of one
	// Intent are a DELIBERATE declaration (rings), not a duplicate file. The separate,
	// temporary refusal of coexisting versions lives in validateSingleIntentVersion so the
	// contract release deletes one named function instead of editing this key back.
	return versionedRef(in.Name, in.Version), in, nil
}

// validateSingleIntentVersion refuses two coexisting versions of one Intent name for as long
// as migration 00041's EXPAND half is the whole story (ADR-0119 D7).
//
// The restriction is real and belongs here rather than being discovered at write time:
// graph.intent still carries its original (name) PRIMARY KEY beside the new unique
// (name, version) index, because ADR-0078 runs migrations while the previous release's
// replicas — which write ON CONFLICT (name) — are still serving. So the second version of a
// name would satisfy the ON CONFLICT target and then be rejected by the surviving PK with a
// raw duplicate-key error from Postgres, at Apply, naming a constraint the author has never
// heard of. That is the §1.8 failure this replaces: a refusal that names the mechanism and
// the way forward.
//
// DELETE THIS FUNCTION AND ITS CALL in the contract release, once every replica is new and
// 00042 has promoted (name, version) to the primary key. Nothing else in the loader needs to
// change then — that is the point of keeping the dedup key versioned above.
func validateSingleIntentVersion(intents []types.Intent) error {
	seen := map[string]int{} // Intent name → the version already declared for it
	for _, in := range intents {
		prev, dup := seen[in.Name]
		if dup {
			lo, hi := min(prev, in.Version), max(prev, in.Version)
			return fmt.Errorf("desiredstate: intent %s is declared at both version %d and version %d, "+
				"and two versions of one Intent cannot coexist yet: graph.intent still holds the (name) "+
				"primary key from before ADR-0119's contract migration, so the second would fail to store. "+
				"Rings light up when that migration ships. Until then promote in ONE commit — retire "+
				"%s@%d, declare %s@%d, and repin every Assignment that names it in the same change, which "+
				"the pinned-version guard permits precisely because no declared Assignment is left pinning "+
				"the retired version",
				in.Name, lo, hi, in.Name, lo, in.Name, hi)
		}
		seen[in.Name] = in.Version
	}
	return nil
}

// ValidateIntent checks one Intent declaration (ADR-0023, ADR-0030). The kind
// is "implemented" iff it has a registered spec schema; the spec is validated
// at its seam (§1.1) and onRemove is gated per-kind (schema-driven removal
// semantics live in the kind, not tribal memory — §2.4).
func ValidateIntent(in types.Intent) error {
	if in.Name == "" {
		return fmt.Errorf("intent requires a name")
	}
	if in.Version < 0 {
		return fmt.Errorf("intent %s: version must be a positive integer, got %d", in.Name, in.Version)
	}
	// ADR-0119 D3: versionable iff a seam exists to carry the pin. An Assignment pins
	// application-shaped Intents; provisioning kinds are selected BY NAME by the provisioning
	// reconcile, which has no Assignment, so a version there could never be selected — and two
	// versions of one fleet would be two claims on the same instance identities, which ADR-0058 D5
	// rejects as a collision. Refused here rather than failing later in provision.Plan, which would
	// blame the wrong document (§1.8).
	//
	// The predicate is DERIVED from the kind constants (types.AssignableIntentKind), so a new
	// provisioning kind inherits this refusal instead of needing someone to remember.
	if in.Version > 0 && !types.AssignableIntentKind(in.Kind) {
		return fmt.Errorf(
			"intent %s: kind %s cannot carry a version — it is selected by NAME by the provisioning "+
				"reconcile, which has no Assignment to pin a version with, and two versions of one fleet "+
				"would be two claims on the same instance identities (ADR-0119 D3, ADR-0058 D5)",
			in.Name, in.Kind)
	}
	specRaw, err := json.Marshal(in.Spec)
	if err != nil {
		return fmt.Errorf("intent %s: marshal spec: %w", in.Name, err)
	}
	// PARTIAL, not complete (ADR-0118 D1): an Intent may legitimately omit a field it
	// leaves to its Assignment's values, so completeness is judged once on the MERGED spec
	// at compile (compiler.validateResolvedSpec). Every field that IS present is still
	// typed here, and an unimplemented kind is still rejected here.
	covered, err := contract.ValidateIntentSpecPartial(in.Kind, specRaw)
	if err != nil {
		return fmt.Errorf("intent %s: %w", in.Name, err)
	}
	if !covered {
		return fmt.Errorf("intent %s: kind %q is not an implemented Intent kind (no spec schema)", in.Name, in.Kind)
	}
	return validateOnRemove(in.Name, in.Kind, in.OnRemove)
}

// validateOnRemove gates the withdrawal lifecycle per Intent kind (§2.4).
// retain is universal. remove (destructive decommission) is implemented for
// Certificate (revoke-or-expire, ADR-0030) and Access (revoke a granted access,
// ADR-0036). revert (restore prior state) is implemented for FileSet (remove
// the distributed file) and Access (ADR-0036). Both surface the Blueprint's
// removeWorkflow on the orphan Finding — a ref the operator launches, never
// auto-run (§5 Flow 2). Withdrawn-but-retained state always raises an orphan
// Finding regardless (§2.4 — abandoned state is never silent).
func validateOnRemove(name, kind, onRemove string) error {
	switch onRemove {
	case "", types.OnRemoveRetain:
		return nil
	case types.OnRemoveRemove:
		switch kind {
		case types.IntentCertificate, types.IntentAccess:
			return nil
		case types.IntentCompute, types.IntentSubnet:
			// Provisioning kinds: onRemove:remove routes to the desired-state decommission reach-path
			// (ADR-0114 D4) — a gated teardown, never an auto-destroy. Compute count-down is fully
			// wired; whole-Intent withdrawal reuses the shipped retain limitation (booked follow-up).
			return nil
		}
		return fmt.Errorf("intent %s: onRemove %q is not implemented for kind %s", name, onRemove, kind)
	case types.OnRemoveRevert:
		switch kind {
		case types.IntentFileSet, types.IntentAccess:
			return nil
		}
		return fmt.Errorf("intent %s: onRemove %q is not implemented for kind %s", name, onRemove, kind)
	default:
		return fmt.Errorf("intent %s: unknown onRemove %q (retain|revert|remove)", name, onRemove)
	}
}

// assignmentFile is the assignments/*.yaml shape. blueprint is "name@version".
type assignmentFile struct {
	Name         string         `yaml:"name"`
	Intent       string         `yaml:"intent"`
	View         string         `yaml:"view"`
	Blueprint    string         `yaml:"blueprint"`
	Environments []string       `yaml:"environments"`
	MaxDelta     *float64       `yaml:"maxDelta"`
	AckDelta     int            `yaml:"ackDelta"`
	Values       map[string]any `yaml:"values"`
}

func parseAssignmentFile(path string, raw []byte) (string, types.Assignment, error) {
	var f assignmentFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.Assignment{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	name, version, err := splitVersionedRef("blueprint", f.Blueprint)
	if err != nil {
		return "", types.Assignment{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	// `intent: tls-app@3` — the same grammar, equally required (ADR-0119 D2). An Assignment pins
	// BOTH halves of what it means: the WHAT and the HOW.
	iname, iversion, ierr := splitVersionedRef("intent", f.Intent)
	if ierr != nil {
		return "", types.Assignment{}, fmt.Errorf("desiredstate: %s: %w", path, ierr)
	}
	a := types.Assignment{
		Name: f.Name, Intent: iname, IntentVersion: iversion, View: f.View,
		Blueprint: name, BlueprintVersion: version,
		Environments: f.Environments, MaxDelta: f.MaxDelta, AckDelta: f.AckDelta,
		Values: f.Values,
	}
	if err := ValidateAssignment(a); err != nil {
		return "", types.Assignment{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return a.Name, a, nil
}

// guardPinnedVersions turns an update-or-delete of a PINNED version into a plan error (ADR-0119 D6).
//
// "Pinned" means some declared Assignment names it. That is deliberately the DECLARED set, not the
// stored one: the question is whether this commit still depends on the version, so removing the
// Assignment and the version it pinned in one commit is legal — the pin goes away in the same
// change that removes the target.
//
// The error is on the PlanEntry rather than returned, so `stratt plan` shows every offending
// document at once instead of stopping at the first (§1.8: an operator fixing a promotion wants the
// whole list).
func guardPinnedVersions(plan *Plan, decls Declarations) error {
	pinnedIntents := map[string][]string{} // name@version → assignments pinning it
	pinnedBlueprints := map[string][]string{}
	for _, a := range decls.Assignments {
		if a.Intent != "" {
			k := versionedRef(a.Intent, a.IntentVersion)
			pinnedIntents[k] = append(pinnedIntents[k], a.Name)
		}
		if a.Blueprint != "" {
			k := versionedRef(a.Blueprint, a.BlueprintVersion)
			pinnedBlueprints[k] = append(pinnedBlueprints[k], a.Name)
		}
	}
	for i := range plan.Entries {
		e := &plan.Entries[i]
		if e.Action != ActionUpdate && e.Action != ActionDelete {
			continue
		}
		var by []string
		switch e.Kind {
		case KindIntent:
			by = pinnedIntents[e.Name]
		case KindBlueprint:
			by = pinnedBlueprints[e.Name]
		default:
			continue
		}
		if len(by) == 0 {
			continue
		}
		sort.Strings(by)
		verb := "edited in place"
		if e.Action == ActionDelete {
			verb = "removed"
		}
		e.Error = fmt.Sprintf(
			"published %s %s is pinned by assignment(s) %v and cannot be %s — declare a new version "+
				"instead (ADR-0119 D6: a pinned configuration is immutable, or promotion means nothing). "+
				"To retire this version, remove or repoint those Assignments in the same change",
			e.Kind, e.Name, by, verb)
	}
	return nil
}

// splitVersionedRef parses the `name@version` grammar shared by `blueprint:` and `intent:`
// (ADR-0119 D2). One grammar, one implementation, one error shape — `kind` only names the field in
// the message. Required for both: an optional pin would leave an environment's identity unstated in
// its own Assignment, and a parser with two requiredness rules is a grammar you have to explain
// rather than read.
func splitVersionedRef(kind, ref string) (string, int, error) {
	at := strings.LastIndex(ref, "@")
	if at <= 0 || at == len(ref)-1 {
		return "", 0, fmt.Errorf("%s must be name@version, got %q", kind, ref)
	}
	version, err := strconv.Atoi(ref[at+1:])
	if err != nil || version < 1 {
		return "", 0, fmt.Errorf("%s version must be a positive integer, got %q", kind, ref[at+1:])
	}
	return ref[:at], version, nil
}

// versionedRef renders the same grammar — the plan key and the wire form of a versioned CaC
// document's identity.
func versionedRef(name string, version int) string {
	if version < 1 {
		version = 1
	}
	return fmt.Sprintf("%s@%d", name, version)
}

// splitBlueprintRef is retained as the Blueprint-named spelling of splitVersionedRef.
func splitBlueprintRef(ref string) (string, int, error) { return splitVersionedRef("blueprint", ref) }

// ValidateAssignment checks one Assignment declaration (ADR-0023). Full
// cross-reference validation (View is cac; Intent/Blueprint/Workflow exist)
// runs at compile, where the graph is available.
func ValidateAssignment(a types.Assignment) error {
	if a.Name == "" {
		return fmt.Errorf("assignment requires a name")
	}
	if a.Intent == "" || a.View == "" || a.Blueprint == "" {
		return fmt.Errorf("assignment %s: intent, view, and blueprint are required", a.Name)
	}
	// Both pins are required (ADR-0119 D2), and this check exists because the YAML and API doors
	// reach here by different routes: the parser gets the version from splitVersionedRef, which
	// cannot yield less than 1, while the admission path JSON-round-trips a wire struct where the
	// field can simply be absent. Without this, the API would accept an unpinned Assignment that
	// Git refuses — a one-surface difference in what the same document means, which is the §1.6
	// asymmetry this codebase has produced twice already.
	if a.IntentVersion < 1 {
		return fmt.Errorf(
			"assignment %s: intentVersion is required (declare `intent: %s@N`) — an environment's "+
				"configuration identity must be stated in its own Assignment, not implied", a.Name, a.Intent)
	}
	if a.BlueprintVersion < 1 {
		return fmt.Errorf("assignment %s: blueprintVersion is required (declare `blueprint: %s@N`)", a.Name, a.Blueprint)
	}
	if a.MaxDelta != nil && (*a.MaxDelta <= 0 || *a.MaxDelta > 1) {
		return fmt.Errorf("assignment %s: maxDelta must be in (0, 1]", a.Name)
	}
	if a.AckDelta < 0 {
		return fmt.Errorf("assignment %s: ackDelta must be >= 0", a.Name)
	}
	if err := rejectEnvKeyedValues(a); err != nil {
		return err
	}
	return nil
}

// rejectEnvKeyedValues refuses the shape `values: {prod: {...}, staging: {...}}`
// (ADR-0118 D1). Environment-conditional config values are the new-configuration-language
// non-goal, and `types.EnvScoped` already binds it: environments is a boolean MEMBERSHIP
// filter, "never a source of env-conditional values". The compliant shape is one
// Assignment per environment, each carrying flat values.
//
// The check is deliberately narrow rather than clever: it fires only when a TOP-LEVEL
// values key equals one of THIS Assignment's own declared environments, which is the
// shape someone actually writes when reaching for a conditional. It is a guardrail, not
// a proof — a nested env-keyed map, or one keyed by an environment this Assignment does
// not declare, is not detectable here and is caught only by review.
func rejectEnvKeyedValues(a types.Assignment) error {
	if len(a.Values) == 0 || len(a.Environments) == 0 {
		return nil
	}
	for _, env := range a.Environments {
		if _, ok := a.Values[env]; ok {
			return fmt.Errorf(
				"assignment %s: values has a top-level key %q matching one of this Assignment's own "+
					"environments — env-conditional config values are forbidden (§2.4/§1 non-goal; "+
					"environments is a membership filter, not a value selector). Declare one Assignment "+
					"per environment, each with flat values, instead",
				a.Name, env)
		}
	}
	return nil
}

// blueprintFile is the blueprints/*.yaml shape.
type blueprintFile struct {
	Name                string           `yaml:"name"`
	Version             int              `yaml:"version"`
	For                 string           `yaml:"for"`
	Defaults            map[string]any   `yaml:"defaults"`
	Routes              []blueprintRoute `yaml:"routes"`
	Severity            string           `yaml:"severity"`
	DampingObservations int              `yaml:"dampingObservations"`
	RemoveWorkflow      string           `yaml:"removeWorkflow"`
	RemoveParams        map[string]any   `yaml:"removeParams"`
}
type blueprintRoute struct {
	Match               []declFacetPred `yaml:"match"`
	Observe             declExpectation `yaml:"observe"`
	Claim               string          `yaml:"claim"`
	RemediationWorkflow string          `yaml:"remediationWorkflow"`
	RemediationParams   map[string]any  `yaml:"remediationParams"`
}
type declFacetPred struct {
	Namespace string `yaml:"namespace"`
	Path      string `yaml:"path"`
	Equals    any    `yaml:"equals"`
}
type declExpectation struct {
	Namespace string `yaml:"namespace"`
	Path      string `yaml:"path"`
	Equals    any    `yaml:"equals"`
	Contains  any    `yaml:"contains"`
	NotBefore string `yaml:"notBefore"`
}

func parseBlueprintFile(path string, raw []byte) (string, types.Blueprint, error) {
	var f blueprintFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.Blueprint{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	b := types.Blueprint{
		Name: f.Name, Version: f.Version, For: f.For,
		Defaults: f.Defaults,
		Severity: f.Severity, DampingObservations: f.DampingObservations,
		RemoveWorkflow: f.RemoveWorkflow, RemoveParams: f.RemoveParams,
	}
	for i, r := range f.Routes {
		var match []types.FacetPredicate
		for _, m := range r.Match {
			eq, err := marshalYAMLValue(m.Equals)
			if err != nil {
				return "", types.Blueprint{}, fmt.Errorf("desiredstate: %s: route %d match: %w", path, i, err)
			}
			match = append(match, types.FacetPredicate{Namespace: m.Namespace, Path: m.Path, Equals: eq})
		}
		eq, err := marshalYAMLValue(r.Observe.Equals)
		if err != nil {
			return "", types.Blueprint{}, fmt.Errorf("desiredstate: %s: route %d observe equals: %w", path, i, err)
		}
		con, err := marshalYAMLValue(r.Observe.Contains)
		if err != nil {
			return "", types.Blueprint{}, fmt.Errorf("desiredstate: %s: route %d observe contains: %w", path, i, err)
		}
		b.Routes = append(b.Routes, types.BlueprintRoute{
			Match: match,
			Observe: types.FacetExpectation{
				Namespace: r.Observe.Namespace, Path: r.Observe.Path,
				Equals: eq, Contains: con, NotBefore: r.Observe.NotBefore,
			},
			Claim: r.Claim, RemediationWorkflow: r.RemediationWorkflow,
			RemediationParams: r.RemediationParams,
		})
	}
	if err := ValidateBlueprint(b); err != nil {
		return "", types.Blueprint{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	// Dedup key is name@version — two versions of one Blueprint coexist.
	return fmt.Sprintf("%s@%d", b.Name, b.Version), b, nil
}

// marshalYAMLValue converts a yaml-decoded value to canonical JSON (nil → nil).
func marshalYAMLValue(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// ValidateBlueprint checks one Blueprint declaration (ADR-0023).
func ValidateBlueprint(b types.Blueprint) error {
	if b.Name == "" || b.Version < 1 {
		return fmt.Errorf("blueprint requires a name and version >= 1")
	}
	if ok, err := contract.HasIntentKind(b.For); err != nil {
		return fmt.Errorf("blueprint %s@%d: %w", b.Name, b.Version, err)
	} else if !ok {
		return fmt.Errorf("blueprint %s@%d: for %q is not an implemented Intent kind", b.Name, b.Version, b.For)
	}
	// G6 (ADR-0083 §5): the Blueprint's defaults reach the compiled Baseline via the
	// overlay merge, so they must cross the §1.1 Contract seam like an Intent's own spec
	// does — validated partial-tolerant (defaults are a subset the Intent completes).
	if len(b.Defaults) > 0 {
		raw, err := json.Marshal(b.Defaults)
		if err != nil {
			return fmt.Errorf("blueprint %s@%d: marshal defaults: %w", b.Name, b.Version, err)
		}
		if _, err := contract.ValidateIntentSpecPartial(b.For, raw); err != nil {
			return fmt.Errorf("blueprint %s@%d: defaults: %w", b.Name, b.Version, err)
		}
	}
	switch b.Severity {
	case "", types.SeverityInfo, types.SeverityWarning, types.SeverityCritical:
	default:
		return fmt.Errorf("blueprint %s@%d: severity must be info, warning, or critical", b.Name, b.Version)
	}
	if len(b.Routes) == 0 {
		return fmt.Errorf("blueprint %s@%d: at least one route is required", b.Name, b.Version)
	}
	for i, r := range b.Routes {
		if r.Observe.Namespace == "" {
			return fmt.Errorf("blueprint %s@%d: route %d observe requires a namespace", b.Name, b.Version, i)
		}
		if len(r.Observe.Equals) == 0 && len(r.Observe.Contains) == 0 && r.Observe.NotBefore == "" {
			return fmt.Errorf("blueprint %s@%d: route %d observe requires equals, contains, or notBefore", b.Name, b.Version, i)
		}
		switch r.Claim {
		case types.ClaimExclusive, types.ClaimAdditive:
		default:
			return fmt.Errorf("blueprint %s@%d: route %d claim must be exclusive or additive", b.Name, b.Version, i)
		}
	}
	return nil
}

// validateParamsContract checks actuation params against the Actuator's
// input Contract (§1.5, ADR-0015) — a bad declaration fails its file at
// plan/reconcile time, never at dispatch. Params carrying {{...}} bindings
// (ADR-0024) are validated at LAUNCH against their resolved values instead
// (the placeholder isn't the value the schema must accept), so their
// contract check is skipped here.
// ValidateOption tunes a declaration validator. It exists so a validator can be
// given estate-wide context it cannot get from the single declaration in front of
// it, without breaking every caller that has none.
type ValidateOption func(*validateOpts)

type validateOpts struct {
	// identities maps a declared Actuator NAME to its pluginIdentity. Needed
	// because an input Contract belongs to the tool, not to the local name an
	// estate gives one of its Actuators — see contract.ValidateActuatorParamsFor.
	identities map[string]string
}

func apply(opts []ValidateOption) validateOpts {
	var o validateOpts
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// WithActuatorIdentities supplies the estate's Actuator name → pluginIdentity map,
// so a Step naming a declared Actuator variant (e.g. an ansible Actuator bound to a
// content-bearing EE, ADR-0117 D3a) resolves its plugin's input Contract instead of
// being rejected as uncontracted. Without it, validation falls back to the name —
// correct for boot-registered Actuators, whose name IS their identity.
func WithActuatorIdentities(m map[string]string) ValidateOption {
	return func(o *validateOpts) { o.identities = m }
}

func validateParamsContract(actuator string, params map[string]any, opts ...ValidateOption) error {
	// A View actuation names its Actuator EXPLICITLY (no platform default, ADR-0046):
	// this validator is reached only on the view-actuation branch (actions, gates, and
	// facet-observation baselines never call it), so an empty actuator is an
	// under-specified declaration — reject it at parse, not silently default it.
	if actuator == "" {
		return fmt.Errorf("a View actuation requires an explicit actuator (no platform default)")
	}
	if template.Has(params) {
		return nil
	}
	raw := json.RawMessage(`{}`)
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("params: %w", err)
		}
		raw = b
	}
	return contract.ValidateActuatorParamsFor(actuator, apply(opts).identities[actuator], raw)
}

// validateActionParamsContract checks an Action Step's params against the
// Action's input Contract (ADR-0031). Template-carrying params ({{.steps.x}} /
// {{.event.x}}) are validated at LAUNCH against resolved values, skipped here.
func validateActionParamsContract(action string, params map[string]any) error {
	if template.Has(params) {
		return nil
	}
	raw := json.RawMessage(`{}`)
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("params: %w", err)
		}
		raw = b
	}
	return contract.ValidateActionInput(action, raw)
}

// checkLaunchFields rejects a {{.launch.X}} binding whose X is not a declared input
// (ADR-0118 D2) — the field-wise half of binding validation.
//
// checkTemplateNamespaces answers "may this context bind `launch` at all"; this answers
// "does `launch.commonName` exist". Without it, a Workflow could publish a typed launch
// interface and still reference a field nothing supplies, which is the same
// declared-but-unsatisfiable shape ADR-0117 kept finding.
//
// Nested access binds its ROOT: {{.launch.tls.minVersion}} requires an input named `tls`
// (an object-typed one). Whether the nested path exists inside it is the input schema's
// business at launch, not this check's.
func checkLaunchFields(what, workflow string, declared map[string]bool, vals ...any) error {
	for path := range template.Paths(vals) {
		root, rest, found := strings.Cut(path, ".")
		if root != "launch" || !found {
			continue
		}
		field, _, _ := strings.Cut(rest, ".")
		if len(declared) == 0 {
			return fmt.Errorf(
				"%s: binds {{.launch.%s}} but workflow %s declares no `inputs` — declare the launch interface, "+
					"or the value can never be supplied", what, rest, workflow)
		}
		if !declared[field] {
			names := make([]string, 0, len(declared))
			for n := range declared {
				names = append(names, n)
			}
			sort.Strings(names)
			return fmt.Errorf(
				"%s: binds {{.launch.%s}}, but %q is not a declared input of workflow %s (declared: %v)",
				what, rest, field, workflow, names)
		}
	}
	return nil
}

// checkTemplateNamespaces rejects a declaration whose bindings reference a
// namespace the context does not provide (ADR-0024): e.g. {{.event.x}} on a
// schedule Trigger, or {{.spec.x}} anywhere outside the compiler.
func checkTemplateNamespaces(what string, allowed map[string]bool, vals ...any) error {
	refs := map[string]bool{}
	for _, v := range vals {
		for ns := range template.References(v) {
			refs[ns] = true
		}
	}
	for ns := range refs {
		if !allowed[ns] {
			return fmt.Errorf("%s: template references the %q namespace, which is not available here", what, ns)
		}
	}
	return nil
}

// workflowFile is the workflows/*.yaml shape (ADR-0011): a DAG of Steps —
// each an actuation or a Gate — with needs-edges and when-conditions.
type workflowFile struct {
	Name  string     `yaml:"name"`
	Steps []stepYAML `yaml:"steps"`
	// Inputs is the launch interface as a JSON Schema object (ADR-0118 D2). Declared in
	// YAML, canonicalized to JSON for the validator — yaml.v3 ignores json tags, so it is
	// read as a generic map and marshalled, exactly like a route's `equals`.
	Inputs map[string]any `yaml:"inputs"`
	// AdoptedFrom is the adopt lineage (ADR-0087) — present on Workflows materialized by
	// `stratt adopt`, read by the standing cutover reconciler. yaml.v3 ignores json tags,
	// so this mirrors types.AdoptedFrom with yaml tags.
	AdoptedFrom *adoptedFromYAML `yaml:"adoptedFrom"`
}
type adoptedFromYAML struct {
	Kind     string `yaml:"kind"`
	Identity string `yaml:"identity"`
	Source   string `yaml:"source"`
}
type stepYAML struct {
	Name            string         `yaml:"name"`
	Needs           []string       `yaml:"needs"`
	When            string         `yaml:"when"`
	Gate            *gateYAML      `yaml:"gate"`
	Policy          *policyYAML    `yaml:"policy"`
	ViewName        string         `yaml:"viewName"`
	Actuator        string         `yaml:"actuator"`
	Action          string         `yaml:"action"`
	DryRun          bool           `yaml:"dryRun"`
	Params          map[string]any `yaml:"params"`
	Slices          int            `yaml:"slices"`
	CredentialRefs  []string       `yaml:"credentialRefs"`
	FacetWriteScope []string       `yaml:"facetWriteScope"`
}
type gateYAML struct {
	Approvers struct {
		Principals []string `yaml:"principals"`
		Teams      []string `yaml:"teams"`
	} `yaml:"approvers"`
	TimeoutSeconds int `yaml:"timeoutSeconds"`
	Threshold      int `yaml:"threshold"` // M-of-N quorum (ADR-0071)
}

// policyYAML is the estate declaration surface for a policy checkpoint Step
// (ADR-0063/0067–0071): the CEL provider's inline-Control dialect. yaml.v3 does
// not read json tags, so these mirror types/policy.go with yaml tags. The typed
// primitives are optional pointers; controlYAML maps 1:1 to types.Control.
type policyYAML struct {
	Controls []controlYAML `yaml:"controls"`
}
type controlYAML struct {
	ID          string           `yaml:"id"`
	Type        string           `yaml:"type"`
	When        string           `yaml:"when"`
	Outcome     string           `yaml:"outcome"`
	Obligations []obligationYAML `yaml:"obligations"`
	TimeWindow  *timeWindowYAML  `yaml:"timeWindow"`
	SoD         *sodYAML         `yaml:"sod"`
	Waiver      *waiverYAML      `yaml:"waiver"`
	BreakGlass  *breakGlassYAML  `yaml:"breakGlass"`
}
type obligationYAML struct {
	Type   string         `yaml:"type"`
	Params map[string]any `yaml:"params"`
}
type timeWindowYAML struct {
	Mode         string   `yaml:"mode"`
	Days         []string `yaml:"days"`
	StartHourUTC int      `yaml:"startHourUtc"`
	EndHourUTC   int      `yaml:"endHourUtc"`
}
type sodYAML struct {
	DistinctFrom []string `yaml:"distinctFrom"`
}
type waiverYAML struct {
	ControlRef    string    `yaml:"controlRef"`
	ExpiresAt     time.Time `yaml:"expiresAt"`
	Justification string    `yaml:"justification"`
	ApprovedBy    string    `yaml:"approvedBy"`
}
type breakGlassYAML struct {
	Bypasses     []string `yaml:"bypasses"`
	PostReviewBy string   `yaml:"postReviewBy"`
}

// toPolicySpec maps the declared policy YAML into the typed PolicySpec. Load
// validation (ValidateWorkflow → the CEL provider's dialect check) runs after.
func toPolicySpec(p *policyYAML) *types.PolicySpec {
	ctrls := make([]types.Control, 0, len(p.Controls))
	for _, c := range p.Controls {
		tc := types.Control{ID: c.ID, Type: c.Type, When: c.When, Outcome: c.Outcome}
		for _, o := range c.Obligations {
			tc.Obligations = append(tc.Obligations, types.Obligation{Type: o.Type, Params: o.Params})
		}
		if c.TimeWindow != nil {
			tc.TimeWindow = &types.TimeWindowSpec{
				Mode: c.TimeWindow.Mode, Days: c.TimeWindow.Days,
				StartHourUTC: c.TimeWindow.StartHourUTC, EndHourUTC: c.TimeWindow.EndHourUTC,
			}
		}
		if c.SoD != nil {
			tc.SoD = &types.SoDSpec{DistinctFrom: c.SoD.DistinctFrom}
		}
		if c.Waiver != nil {
			tc.Waiver = &types.WaiverSpec{
				ControlRef: c.Waiver.ControlRef, ExpiresAt: c.Waiver.ExpiresAt,
				Justification: c.Waiver.Justification, ApprovedBy: c.Waiver.ApprovedBy,
			}
		}
		if c.BreakGlass != nil {
			tc.BreakGlass = &types.BreakGlassSpec{Bypasses: c.BreakGlass.Bypasses, PostReviewBy: c.BreakGlass.PostReviewBy}
		}
		ctrls = append(ctrls, tc)
	}
	return &types.PolicySpec{Controls: ctrls}
}

func parseWorkflowFile(path string, raw []byte, opts ...ValidateOption) (string, types.Workflow, error) {
	var f workflowFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.Workflow{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	w := types.Workflow{Name: f.Name}
	// The launch interface is authored in YAML and consumed as JSON Schema, so it is
	// canonicalized here — the same YAML→JSON step a route's `equals` takes (ADR-0118 D2).
	if f.Inputs != nil {
		raw, err := json.Marshal(f.Inputs)
		if err != nil {
			return "", types.Workflow{}, fmt.Errorf("desiredstate: %s: inputs: %w", path, err)
		}
		w.Inputs = raw
	}
	if f.AdoptedFrom != nil {
		w.AdoptedFrom = &types.AdoptedFrom{
			Kind: f.AdoptedFrom.Kind, Identity: f.AdoptedFrom.Identity, Source: f.AdoptedFrom.Source,
		}
	}
	for _, s := range f.Steps {
		step := types.Step{
			Name: s.Name, Needs: s.Needs, When: s.When,
			ViewName: s.ViewName, Actuator: s.Actuator,
			Action: s.Action, DryRun: s.DryRun, Params: s.Params,
			Slices: s.Slices, CredentialRefs: s.CredentialRefs,
			FacetWriteScope: s.FacetWriteScope,
		}
		if s.Gate != nil {
			step.Gate = &types.GateSpec{
				Approvers: types.GateApprovers{
					Principals: s.Gate.Approvers.Principals,
					Teams:      s.Gate.Approvers.Teams,
				},
				TimeoutSeconds: s.Gate.TimeoutSeconds,
				Threshold:      s.Gate.Threshold,
			}
		}
		if s.Policy != nil {
			step.Policy = toPolicySpec(s.Policy)
		}
		w.Steps = append(w.Steps, step)
	}
	if err := ValidateWorkflow(w, opts...); err != nil {
		return "", types.Workflow{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return w.Name, w, nil
}

// ValidateWorkflow checks one Workflow declaration; exported for the API's
// desired-state plan/apply path (same document shape as the Git checkout).
func ValidateWorkflow(w types.Workflow, opts ...ValidateOption) error {
	if w.Name == "" || len(w.Steps) == 0 {
		return fmt.Errorf("workflow requires name and at least one step")
	}
	// The launch interface is compiled ONCE per Workflow (ADR-0118 D2): a malformed or
	// non-closed input schema fails the declaration, not the first launch.
	if _, err := contract.CompileInputSchema(w.Name, w.Inputs); err != nil {
		return err
	}
	declaredInputs, err := contract.InputNames(w.Inputs)
	if err != nil {
		return fmt.Errorf("workflow %s: inputs: %w", w.Name, err)
	}
	byName := map[string]types.Step{}
	for _, s := range w.Steps {
		if s.Name == "" {
			return fmt.Errorf("workflow %s: every step requires a name", w.Name)
		}
		if _, dup := byName[s.Name]; dup {
			return fmt.Errorf("workflow %s: duplicate step %q", w.Name, s.Name)
		}
		byName[s.Name] = s
		switch s.When {
		case "", types.WhenSuccess, types.WhenFailure, types.WhenAlways:
		default:
			return fmt.Errorf("workflow %s: step %s: when must be success, failure, or always", w.Name, s.Name)
		}
		if s.When != "" && s.When != types.WhenSuccess && len(s.Needs) == 0 {
			return fmt.Errorf("workflow %s: step %s: when %s requires needs", w.Name, s.Name, s.When)
		}
		// A Step is exactly one of four shapes (§2.3, ADR-0031, ADR-0063): a Gate,
		// a Policy checkpoint, an Action (targetless typed operation), or an
		// Actuation (Actuator+View).
		isGate := s.Gate != nil
		isPolicy := s.Policy != nil
		isAction := s.Action != ""
		isActuation := s.ViewName != "" || s.Actuator != "" || s.Slices != 0
		switch {
		case isPolicy && (isGate || isAction || isActuation),
			isGate && (isAction || isActuation):
			return fmt.Errorf("workflow %s: step %s: a step is a gate, a policy, an action, or an actuation — not multiple", w.Name, s.Name)
		case isAction && isActuation:
			return fmt.Errorf("workflow %s: step %s: a step is an action or an actuation, not both (actions are targetless — no viewName/actuator/slices)", w.Name, s.Name)
		case !isGate && !isPolicy && !isAction && s.ViewName == "":
			return fmt.Errorf("workflow %s: step %s: actuation step requires viewName", w.Name, s.Name)
		case isGate && len(s.Gate.Approvers.Principals) == 0 && len(s.Gate.Approvers.Teams) == 0:
			return fmt.Errorf("workflow %s: step %s: gate requires approvers (principals and/or teams)", w.Name, s.Name)
		case isGate && s.Gate.TimeoutSeconds < 0:
			return fmt.Errorf("workflow %s: step %s: gate timeoutSeconds must be >= 0", w.Name, s.Name)
		case isGate && s.Gate.Threshold < 0:
			return fmt.Errorf("workflow %s: step %s: gate threshold must be >= 0", w.Name, s.Name)
		case isPolicy && len(s.Policy.Controls) == 0:
			return fmt.Errorf("workflow %s: step %s: policy step requires controls", w.Name, s.Name)
		case !isGate && !isPolicy && s.Slices < 0:
			return fmt.Errorf("workflow %s: step %s: slices must be >= 0", w.Name, s.Name)
		}
		// A policy Step's control predicates are CEL-compiled at load — fail the
		// file, never silently at decision time (§1.8, ADR-0063).
		if isPolicy {
			if err := policy.ValidateControls(s.Policy.Controls); err != nil {
				return fmt.Errorf("workflow %s: step %s: %w", w.Name, s.Name, err)
			}
		}
		// Params/CredentialRefs may bind the event namespace (firing Emitter,
		// ADR-0024), the steps namespace (a prior Step's outputs, ADR-0031), and
		// the launch namespace (operator-supplied launch params, ADR-0059/0118) — all
		// resolved at launch. Launch values only fill declared placeholders in an
		// already-gated, Contract-bounded Step (§2.5); they cannot move the target.
		bindable := map[string]bool{"event": true, "steps": true, "launch": true}
		// And a {{.launch.X}} binding must name a DECLARED input (ADR-0118 D2). The
		// namespace check above cannot see `{{.launch.comonName}}` — it only knows the
		// namespace is legal — so the typo would survive to dispatch and fail there,
		// far from the file that caused it (§1.8).
		if err := checkLaunchFields(
			fmt.Sprintf("workflow %s step %s", w.Name, s.Name), w.Name, declaredInputs, s.Params); err != nil {
			return err
		}
		switch {
		case isAction:
			if err := validateActionParamsContract(s.Action, s.Params); err != nil {
				return fmt.Errorf("workflow %s: step %s: %w", w.Name, s.Name, err)
			}
			if err := checkTemplateNamespaces(
				fmt.Sprintf("workflow %s step %s", w.Name, s.Name), bindable, s.Params); err != nil {
				return err
			}
		case !isGate && !isPolicy:
			if err := validateParamsContract(s.Actuator, s.Params, opts...); err != nil {
				return fmt.Errorf("workflow %s: step %s: %w", w.Name, s.Name, err)
			}
			if err := checkTemplateNamespaces(
				fmt.Sprintf("workflow %s step %s", w.Name, s.Name), bindable, s.Params); err != nil {
				return err
			}
		}
	}
	// Needs must resolve, and the graph must be acyclic (Kahn's algorithm).
	indegree := map[string]int{}
	for _, s := range w.Steps {
		for _, n := range s.Needs {
			if _, ok := byName[n]; !ok {
				return fmt.Errorf("workflow %s: step %s needs unknown step %q", w.Name, s.Name, n)
			}
			if n == s.Name {
				return fmt.Errorf("workflow %s: step %s needs itself", w.Name, s.Name)
			}
			indegree[s.Name]++
		}
	}
	queue := []string{}
	for _, s := range w.Steps {
		if indegree[s.Name] == 0 {
			queue = append(queue, s.Name)
		}
	}
	visited := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		visited++
		for _, s := range w.Steps {
			for _, n := range s.Needs {
				if n == cur {
					if indegree[s.Name]--; indegree[s.Name] == 0 {
						queue = append(queue, s.Name)
					}
				}
			}
		}
	}
	if visited != len(w.Steps) {
		return fmt.Errorf("workflow %s: step graph has a cycle", w.Name)
	}
	if err := validatePlanPinning(w, byName); err != nil {
		return err
	}
	return nil
}

// validatePlanPinning enforces the compile-validated, FAIL-CLOSED Plan↔Gate↔Apply
// triple (ADR-0047 §8, guardian pass 3): a plan-pinned Apply must be transitively
// guarded by a Gate that binds the SAME Plan step's digest. It closes three holes —
// an unknown/non-Plan PlanFrom, a Gate binding a plan it does not `needs`, and (the
// most dangerous) a plan-pinned Apply with no guarding Gate, which would otherwise
// let the runtime silently degrade to an unpinned live apply of `desired`.
func validatePlanPinning(w types.Workflow, byName map[string]types.Step) error {
	// (1) Every PlanFrom must name an existing PLAN step.
	for _, s := range w.Steps {
		if s.PlanFrom == "" {
			continue
		}
		ref, ok := byName[s.PlanFrom]
		if !ok {
			return fmt.Errorf("workflow %s: step %s: planFrom names unknown step %q", w.Name, s.Name, s.PlanFrom)
		}
		if !ref.Plan {
			return fmt.Errorf("workflow %s: step %s: planFrom %q is not a plan step (missing plan: true)", w.Name, s.Name, s.PlanFrom)
		}
		// (2) A Gate that binds a plan must `needs` it (so the digest exists to bind).
		if s.Gate != nil && !directlyNeeds(s, s.PlanFrom) {
			return fmt.Errorf("workflow %s: gate %s: binds plan %q but does not need it", w.Name, s.Name, s.PlanFrom)
		}
	}
	// (3) A plan-pinned Apply must be transitively guarded by a Gate with the SAME
	// PlanFrom — else the pin is unenforced (fail-closed, never a silent unpinned apply).
	for _, s := range w.Steps {
		applyPinned := s.PlanFrom != "" && s.Gate == nil && !s.Plan
		if !applyPinned {
			continue
		}
		if !guardedByGateForPlan(s.Name, s.PlanFrom, w, byName, map[string]bool{}) {
			return fmt.Errorf("workflow %s: step %s: plan-pinned Apply of %q is not guarded by a Gate binding that plan — a plan-pinned Apply must sit behind its Plan's Gate (ADR-0047 §8, fail-closed)", w.Name, s.Name, s.PlanFrom)
		}
	}
	return nil
}

func directlyNeeds(s types.Step, name string) bool {
	for _, n := range s.Needs {
		if n == name {
			return true
		}
	}
	return false
}

// guardedByGateForPlan reports whether some step in name's transitive needs-closure
// is a Gate whose PlanFrom == plan.
func guardedByGateForPlan(name, plan string, w types.Workflow, byName map[string]types.Step, seen map[string]bool) bool {
	if seen[name] {
		return false
	}
	seen[name] = true
	for _, n := range byName[name].Needs {
		nd := byName[n]
		if nd.Gate != nil && nd.PlanFrom == plan {
			return true
		}
		if guardedByGateForPlan(n, plan, w, byName, seen) {
			return true
		}
	}
	return false
}

func (ds declSelector) toSelector() (types.ViewSelector, error) {
	sel := types.ViewSelector{Kinds: ds.Kinds, Labels: ds.Labels}
	for _, f := range ds.Facets {
		if f.Namespace == "" {
			return sel, fmt.Errorf("facet predicate requires a namespace")
		}
		if f.Equals == nil {
			return sel, fmt.Errorf("facet predicate on %s requires equals", f.Namespace)
		}
		eq, err := json.Marshal(f.Equals)
		if err != nil {
			return sel, fmt.Errorf("facet predicate on %s: %w", f.Namespace, err)
		}
		sel.Facets = append(sel.Facets, types.FacetPredicate{
			Namespace: f.Namespace, Path: f.Path, Equals: json.RawMessage(eq),
		})
	}
	for _, r := range ds.Relations {
		if r.Type == "" {
			return sel, fmt.Errorf("relation predicate requires a type")
		}
		sel.Relations = append(sel.Relations, types.RelationPredicate{
			Type: r.Type, TargetKind: r.TargetKind, TargetLabels: r.TargetLabels,
		})
	}
	return sel, nil
}

// ── plan / apply ─────────────────────────────────────────────────────────────

// ScopeToEnvironment filters the parsed declarations to the apply-set in scope
// for the active environment (ADR-0057), consistent with the store's env-scoped
// list reads (which scope the prune candidates + the compiler). The launching/
// active kinds (Assignment/Trigger/Baseline) carry an environment and are filtered
// here; provider-selection kinds (Actuator/Connector/CapabilityBinding) are also
// env-scoped but filtered at resolution time in the provisioning reach-path
// (ADR-0113 D2, see provisioning_resolve.go), not in this apply-set filter.
// Views/Workflows are targets reached only through a scoped kind and are not
// independently scoped. env == "" (unscoped daemon) keeps everything unchanged.
func ScopeToEnvironment(d Declarations, env string) Declarations {
	if env == "" {
		return d
	}
	assigns := make([]types.Assignment, 0, len(d.Assignments))
	for _, a := range d.Assignments {
		if types.InScope(a.Environments, env) {
			assigns = append(assigns, a)
		}
	}
	trigs := make([]types.Trigger, 0, len(d.Triggers))
	for _, t := range d.Triggers {
		if types.InScope(t.Environments, env) {
			trigs = append(trigs, t)
		}
	}
	bls := make([]types.Baseline, 0, len(d.Baselines))
	for _, b := range d.Baselines {
		if types.InScope(b.Environments, env) {
			bls = append(bls, b)
		}
	}
	d.Assignments, d.Triggers, d.Baselines = assigns, trigs, bls
	return d
}

// ComputePlan diffs the declarations against the graph's current state
// across every declared kind.
func ComputePlan(ctx context.Context, store *graph.Store, decls Declarations) (Plan, error) {
	plan, err := computeViewPlan(ctx, store, decls.Views)
	if err != nil {
		return Plan{}, err
	}
	refPlan, err := computeCredentialRefPlan(ctx, store, decls.CredentialRefs)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, refPlan.Entries...)
	connPlan, err := computeConnectorPlan(ctx, store, decls.Connectors)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, connPlan.Entries...)
	actPlan, err := computeActuatorPlan(ctx, store, decls.Actuators)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, actPlan.Entries...)
	cbPlan, err := computeCapabilityBindingPlan(ctx, store, decls.CapabilityBindings)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, cbPlan.Entries...)
	trigPlan, err := computeTriggerPlan(ctx, store, decls.Triggers)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, trigPlan.Entries...)
	wfPlan, err := computeWorkflowPlan(ctx, store, decls.Workflows)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, wfPlan.Entries...)
	emPlan, err := computeEmitterPlan(ctx, store, decls.Emitters)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, emPlan.Entries...)
	sitePlan, err := computeSitePlan(ctx, store, decls.Sites)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, sitePlan.Entries...)
	cellPlan, err := computeCellPlan(ctx, store, decls.Cells)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, cellPlan.Entries...)
	scimPlan, err := computeScimPlan(ctx, store, decls.SCIMIdPs)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, scimPlan.Entries...)
	nsPlan, err := computeNotifySinkPlan(ctx, store, decls.NotifySinks)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, nsPlan.Entries...)
	subPlan, err := computeSubscriptionPlan(ctx, store, decls.Subscriptions)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, subPlan.Entries...)
	blPlan, err := computeBaselinePlan(ctx, store, decls.Baselines)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, blPlan.Entries...)
	msPlan, err := computeMCPServerPlan(ctx, store, decls.MCPServers)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, msPlan.Entries...)
	inPlan, err := computeIntentLayerPlan(ctx, store, decls)
	if err != nil {
		return Plan{}, err
	}
	plan.Entries = append(plan.Entries, inPlan.Entries...)
	sort.Slice(plan.Entries, func(i, j int) bool {
		if plan.Entries[i].Kind != plan.Entries[j].Kind {
			return plan.Entries[i].Kind < plan.Entries[j].Kind
		}
		return plan.Entries[i].Name < plan.Entries[j].Name
	})
	return plan, nil
}

func computeViewPlan(ctx context.Context, store *graph.Store, decls []Declaration) (Plan, error) {
	cac, err := store.ListViewsDeclaredBy(ctx, graph.DeclaredByCaC)
	if err != nil {
		return Plan{}, err
	}
	api, err := store.ListViewsDeclaredBy(ctx, graph.DeclaredByAPI)
	if err != nil {
		return Plan{}, err
	}
	cacByName := map[string]types.View{}
	for _, v := range cac {
		cacByName[v.Name] = v
	}
	apiByName := map[string]types.View{}
	for _, v := range api {
		apiByName[v.Name] = v
	}

	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindView, Name: d.Name, NewSelector: ptr(d.Selector)}
		switch {
		case !existsIn(cacByName, d.Name) && !existsIn(apiByName, d.Name):
			entry.Action = ActionCreate
		case existsIn(apiByName, d.Name):
			v := apiByName[d.Name]
			entry.Action = ActionAdopt
			entry.OldSelector = ptr(v.Selector)
		default:
			v := cacByName[d.Name]
			if selectorsEqual(v.Selector, d.Selector) {
				entry.Action = ActionNoop
				entry.OldSelector = nil
				entry.NewSelector = nil
			} else {
				entry.Action = ActionUpdate
				entry.OldSelector = ptr(v.Selector)
			}
		}
		if entry.Action != ActionNoop {
			// A parametrized View's membership is a launch-time concept —
			// counting the literal {{.param.x}} selector would print a
			// misleading ~0 (ADR-0024). Mark it, skip the count.
			if selectorHasTemplate(d.Selector) {
				entry.ParamDependent = true
			} else {
				n, err := store.CountSelector(ctx, d.Selector)
				if err != nil {
					return Plan{}, err
				}
				entry.MemberCount = n
			}
		}
		plan.Entries = append(plan.Entries, entry)
	}

	// Prune: cac Views no longer declared. api Views are never candidates.
	for _, v := range cac {
		if declared[v.Name] {
			continue
		}
		e := PlanEntry{Kind: KindView, Name: v.Name, Action: ActionDelete, OldSelector: ptr(v.Selector)}
		if selectorHasTemplate(v.Selector) {
			e.ParamDependent = true
		} else {
			n, err := store.CountSelector(ctx, v.Selector)
			if err != nil {
				return Plan{}, err
			}
			e.MemberCount = n
		}
		plan.Entries = append(plan.Entries, e)
	}
	return plan, nil
}

// selectorHasTemplate reports whether a selector carries {{...}} placeholders
// (a parametrized View, ADR-0024).
func selectorHasTemplate(sel types.ViewSelector) bool {
	for _, v := range sel.Labels {
		if strings.Contains(v, "{{") {
			return true
		}
	}
	for _, f := range sel.Facets {
		if strings.Contains(string(f.Equals), "{{") {
			return true
		}
	}
	return false
}

// computeCredentialRefPlan diffs declared CredentialRef pointers. Equality
// is semantic JSON equality of the pointer document (never material — none
// exists to compare, §2.5). MemberCount is not meaningful for refs.
func computeCredentialRefPlan(ctx context.Context, store *graph.Store, decls []types.CredentialRef) (Plan, error) {
	cac, err := store.ListCredentialRefsDeclaredBy(ctx, graph.DeclaredByCaC)
	if err != nil {
		return Plan{}, err
	}
	api, err := store.ListCredentialRefsDeclaredBy(ctx, graph.DeclaredByAPI)
	if err != nil {
		return Plan{}, err
	}
	cacByName := map[string]types.CredentialRef{}
	for _, r := range cac {
		cacByName[r.Name] = r
	}
	apiByName := map[string]bool{}
	for _, r := range api {
		apiByName[r.Name] = true
	}

	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindCredentialRef, Name: d.Name}
		current, isCac := cacByName[d.Name]
		switch {
		case !isCac && !apiByName[d.Name]:
			entry.Action = ActionCreate
		case apiByName[d.Name]:
			entry.Action = ActionAdopt
		case credentialRefsEqual(current, d):
			entry.Action = ActionNoop
		default:
			entry.Action = ActionUpdate
		}
		plan.Entries = append(plan.Entries, entry)
	}
	for _, r := range cac {
		if !declared[r.Name] {
			plan.Entries = append(plan.Entries, PlanEntry{
				Kind: KindCredentialRef, Name: r.Name, Action: ActionDelete,
			})
		}
	}
	return plan, nil
}

// computeTriggerPlan diffs declared Triggers (CaC-only, ADR-0010: every
// stored Trigger is cac by construction, so there is no adopt case).
// Equality is semantic JSON equality of the declaration document.
func computeTriggerPlan(ctx context.Context, store *graph.Store, decls []types.Trigger) (Plan, error) {
	current, err := store.ListTriggers(ctx)
	if err != nil {
		return Plan{}, err
	}
	byName := map[string]types.Trigger{}
	for _, t := range current {
		byName[t.Name] = t
	}

	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindTrigger, Name: d.Name}
		cur, exists := byName[d.Name]
		switch {
		case !exists:
			entry.Action = ActionCreate
		case triggersEqual(cur, d):
			entry.Action = ActionNoop
		default:
			entry.Action = ActionUpdate
		}
		plan.Entries = append(plan.Entries, entry)
	}
	for _, t := range current {
		if !declared[t.Name] {
			plan.Entries = append(plan.Entries, PlanEntry{
				Kind: KindTrigger, Name: t.Name, Action: ActionDelete,
			})
		}
	}
	return plan, nil
}

// computeWorkflowPlan diffs declared Workflows (CaC-only, ADR-0011 — same
// posture as Triggers: no adopt case, semantic JSON equality).
func computeWorkflowPlan(ctx context.Context, store *graph.Store, decls []types.Workflow) (Plan, error) {
	current, err := store.ListWorkflows(ctx)
	if err != nil {
		return Plan{}, err
	}
	byName := map[string]types.Workflow{}
	for _, w := range current {
		byName[w.Name] = w
	}

	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindWorkflow, Name: d.Name}
		cur, exists := byName[d.Name]
		switch {
		case !exists:
			entry.Action = ActionCreate
		case declDocsEqual(cur, d):
			entry.Action = ActionNoop
		default:
			entry.Action = ActionUpdate
		}
		plan.Entries = append(plan.Entries, entry)
	}
	for _, w := range current {
		if !declared[w.Name] {
			plan.Entries = append(plan.Entries, PlanEntry{
				Kind: KindWorkflow, Name: w.Name, Action: ActionDelete,
			})
		}
	}
	return plan, nil
}

// computeScimPlan diffs declared SCIM IdPs (CaC-only, ADR-0035).
func computeScimPlan(ctx context.Context, store *graph.Store, decls []types.SCIMIdP) (Plan, error) {
	current, err := store.ListIDPs(ctx)
	if err != nil {
		return Plan{}, err
	}
	byName := map[string]types.SCIMIdP{}
	for _, d := range current {
		byName[d.Name] = d
	}
	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindSCIMIdP, Name: d.Name}
		cur, exists := byName[d.Name]
		switch {
		case !exists:
			entry.Action = ActionCreate
		case declDocsEqual(cur, d):
			entry.Action = ActionNoop
		default:
			entry.Action = ActionUpdate
		}
		plan.Entries = append(plan.Entries, entry)
	}
	for _, d := range current {
		if !declared[d.Name] {
			plan.Entries = append(plan.Entries, PlanEntry{Kind: KindSCIMIdP, Name: d.Name, Action: ActionDelete})
		}
	}
	return plan, nil
}

// computeEmitterPlan diffs declared Emitters (CaC-only, ADR-0018).
func computeEmitterPlan(ctx context.Context, store *graph.Store, decls []types.Emitter) (Plan, error) {
	current, err := store.ListEmitters(ctx)
	if err != nil {
		return Plan{}, err
	}
	byName := map[string]types.Emitter{}
	for _, e := range current {
		byName[e.Name] = e
	}
	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindEmitter, Name: d.Name}
		cur, exists := byName[d.Name]
		switch {
		case !exists:
			entry.Action = ActionCreate
		case declDocsEqual(cur, d):
			entry.Action = ActionNoop
		default:
			entry.Action = ActionUpdate
		}
		plan.Entries = append(plan.Entries, entry)
	}
	for _, e := range current {
		if !declared[e.Name] {
			plan.Entries = append(plan.Entries, PlanEntry{Kind: KindEmitter, Name: e.Name, Action: ActionDelete})
		}
	}
	return plan, nil
}

// computeSitePlan diffs declared Sites (CaC-only, ADR-0032).
func computeSitePlan(ctx context.Context, store *graph.Store, decls []types.Site) (Plan, error) {
	current, err := store.ListSites(ctx)
	if err != nil {
		return Plan{}, err
	}
	byName := map[string]types.Site{}
	for _, s := range current {
		byName[s.Name] = s
	}
	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindSite, Name: d.Name}
		cur, exists := byName[d.Name]
		switch {
		case !exists:
			entry.Action = ActionCreate
		case declDocsEqual(cur, d):
			entry.Action = ActionNoop
		default:
			entry.Action = ActionUpdate
		}
		plan.Entries = append(plan.Entries, entry)
	}
	for _, s := range current {
		if !declared[s.Name] {
			plan.Entries = append(plan.Entries, PlanEntry{Kind: KindSite, Name: s.Name, Action: ActionDelete})
		}
	}
	return plan, nil
}

// computeCellPlan diffs declared Cells (CaC-only, ADR-0044) — the federation
// peer registry.
func computeCellPlan(ctx context.Context, store *graph.Store, decls []types.Cell) (Plan, error) {
	current, err := store.ListCells(ctx)
	if err != nil {
		return Plan{}, err
	}
	byName := map[string]types.Cell{}
	for _, c := range current {
		byName[c.Name] = c
	}
	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindCell, Name: d.Name}
		cur, exists := byName[d.Name]
		switch {
		case !exists:
			entry.Action = ActionCreate
		case declDocsEqual(cur, d):
			entry.Action = ActionNoop
		default:
			entry.Action = ActionUpdate
		}
		plan.Entries = append(plan.Entries, entry)
	}
	for _, c := range current {
		if !declared[c.Name] {
			plan.Entries = append(plan.Entries, PlanEntry{Kind: KindCell, Name: c.Name, Action: ActionDelete})
		}
	}
	return plan, nil
}

// computeNotifySinkPlan diffs declared Sinks (CaC-only, ADR-0027).
func computeNotifySinkPlan(ctx context.Context, store *graph.Store, decls []types.Sink) (Plan, error) {
	current, err := store.ListNotifySinks(ctx)
	if err != nil {
		return Plan{}, err
	}
	byName := map[string]types.Sink{}
	for _, s := range current {
		byName[s.Name] = s
	}
	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindNotifySink, Name: d.Name}
		cur, exists := byName[d.Name]
		switch {
		case !exists:
			entry.Action = ActionCreate
		case declDocsEqual(cur, d):
			entry.Action = ActionNoop
		default:
			entry.Action = ActionUpdate
		}
		plan.Entries = append(plan.Entries, entry)
	}
	for _, s := range current {
		if !declared[s.Name] {
			plan.Entries = append(plan.Entries, PlanEntry{Kind: KindNotifySink, Name: s.Name, Action: ActionDelete})
		}
	}
	return plan, nil
}

// computeSubscriptionPlan diffs declared Subscriptions (CaC-only, ADR-0027).
func computeSubscriptionPlan(ctx context.Context, store *graph.Store, decls []types.Subscription) (Plan, error) {
	current, err := store.ListSubscriptions(ctx)
	if err != nil {
		return Plan{}, err
	}
	byName := map[string]types.Subscription{}
	for _, s := range current {
		byName[s.Name] = s
	}
	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindSubscription, Name: d.Name}
		cur, exists := byName[d.Name]
		switch {
		case !exists:
			entry.Action = ActionCreate
		case declDocsEqual(cur, d):
			entry.Action = ActionNoop
		default:
			entry.Action = ActionUpdate
		}
		plan.Entries = append(plan.Entries, entry)
	}
	for _, s := range current {
		if !declared[s.Name] {
			plan.Entries = append(plan.Entries, PlanEntry{Kind: KindSubscription, Name: s.Name, Action: ActionDelete})
		}
	}
	return plan, nil
}

// computeBaselinePlan diffs declared Baselines (CaC-only, ADR-0019 — same
// posture as Triggers: no adopt case, semantic JSON equality).
func computeBaselinePlan(ctx context.Context, store *graph.Store, decls []types.Baseline) (Plan, error) {
	current, err := store.ListBaselines(ctx)
	if err != nil {
		return Plan{}, err
	}
	byName := map[string]types.Baseline{}
	for _, b := range current {
		// Compiler-owned Baselines (ADR-0023) are the Intent compiler's to
		// manage — the hand-written baselines/ kind never touches them.
		if b.CompiledFrom != nil {
			continue
		}
		byName[b.Name] = b
	}
	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindBaseline, Name: d.Name}
		cur, exists := byName[d.Name]
		switch {
		case !exists:
			entry.Action = ActionCreate
		case declDocsEqual(cur, d):
			entry.Action = ActionNoop
		default:
			entry.Action = ActionUpdate
		}
		plan.Entries = append(plan.Entries, entry)
	}
	for name, b := range byName {
		if !declared[name] {
			plan.Entries = append(plan.Entries, PlanEntry{Kind: KindBaseline, Name: b.Name, Action: ActionDelete})
		}
	}
	return plan, nil
}

// computeMCPServerPlan diffs declared MCPServers (CaC-only, ADR-0022).
func computeMCPServerPlan(ctx context.Context, store *graph.Store, decls []types.MCPServer) (Plan, error) {
	current, err := store.ListMCPServers(ctx)
	if err != nil {
		return Plan{}, err
	}
	byName := map[string]types.MCPServer{}
	for _, m := range current {
		byName[m.Name] = m
	}
	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindMCPServer, Name: d.Name}
		cur, exists := byName[d.Name]
		switch {
		case !exists:
			entry.Action = ActionCreate
		case declDocsEqual(cur, d):
			entry.Action = ActionNoop
		default:
			entry.Action = ActionUpdate
		}
		plan.Entries = append(plan.Entries, entry)
	}
	for _, m := range current {
		if !declared[m.Name] {
			plan.Entries = append(plan.Entries, PlanEntry{Kind: KindMCPServer, Name: m.Name, Action: ActionDelete})
		}
	}
	return plan, nil
}

// computeIntentLayerPlan diffs declared Intents, Assignments, and Blueprints
// (CaC-only, ADR-0023). Blueprints are keyed by name@version so versions
// coexist; the plan entry Name carries that key for prune uniqueness.
func computeIntentLayerPlan(ctx context.Context, store *graph.Store, decls Declarations) (Plan, error) {
	var plan Plan

	curIntents, err := store.ListIntents(ctx)
	if err != nil {
		return Plan{}, err
	}
	// Intents are keyed name@version (ADR-0119 D1), exactly as Blueprints are in the sibling block
	// below. The source map is re-keyed with them, because Apply looks the declaration up by
	// PlanEntry.Name — an inByName map would miss every entry the moment the key carries a version.
	//
	// PlanEntry.Name is a WIRE field, so this changes `stratt plan` output and the plan artifact for
	// intents: `intent tls-app@3: update` rather than `intent tls-app: update`. Deliberate — the
	// version is the half of the identity that was previously invisible.
	inByKey := map[string]types.Intent{}
	for _, in := range curIntents {
		inByKey[versionedRef(in.Name, in.Version)] = in
	}
	declaredIn := map[string]bool{}
	for _, d := range decls.Intents {
		k := versionedRef(d.Name, d.Version)
		declaredIn[k] = true
		e := PlanEntry{Kind: KindIntent, Name: k}
		cur, ok := inByKey[k]
		e.Action = diffAction(ok, declDocsEqual(cur, d))
		plan.Entries = append(plan.Entries, e)
	}
	for _, in := range curIntents {
		if k := versionedRef(in.Name, in.Version); !declaredIn[k] {
			plan.Entries = append(plan.Entries, PlanEntry{Kind: KindIntent, Name: k, Action: ActionDelete})
		}
	}

	// ADR-0119 D6 — THE guard that makes "immutable once it passes go" true. Versioning alone does
	// not: UpsertIntent still updates a row in place, and an ActionUpdate on a same-version content
	// edit would change what a pinned environment is running at the next reconcile. So a version a
	// DECLARED Assignment pins may be neither updated nor deleted; the author must declare a new
	// version, which is the reviewable act promotion is built on.
	//
	// Applied to Intents AND Blueprints below, as one rule: Blueprints have had the same exposure
	// since they were versioned (their in-repo layout is one file per name with an inline
	// `version:`, so the natural bump edits in place too), and a second divergent rule for the same
	// situation would be worse than the gap.
	if err := guardPinnedVersions(&plan, decls); err != nil {
		return Plan{}, err
	}

	curAsgs, err := store.ListAssignments(ctx)
	if err != nil {
		return Plan{}, err
	}
	asgByName := map[string]types.Assignment{}
	for _, a := range curAsgs {
		asgByName[a.Name] = a
	}
	declaredAsg := map[string]bool{}
	for _, d := range decls.Assignments {
		declaredAsg[d.Name] = true
		e := PlanEntry{Kind: KindAssignment, Name: d.Name}
		cur, ok := asgByName[d.Name]
		e.Action = diffAction(ok, declDocsEqual(cur, d))
		plan.Entries = append(plan.Entries, e)
	}
	for _, a := range curAsgs {
		if !declaredAsg[a.Name] {
			plan.Entries = append(plan.Entries, PlanEntry{Kind: KindAssignment, Name: a.Name, Action: ActionDelete})
		}
	}

	curBps, err := store.ListBlueprints(ctx)
	if err != nil {
		return Plan{}, err
	}
	bpKey := func(name string, version int) string { return fmt.Sprintf("%s@%d", name, version) }
	bpByKey := map[string]types.Blueprint{}
	for _, b := range curBps {
		bpByKey[bpKey(b.Name, b.Version)] = b
	}
	declaredBp := map[string]bool{}
	for _, d := range decls.Blueprints {
		k := bpKey(d.Name, d.Version)
		declaredBp[k] = true
		e := PlanEntry{Kind: KindBlueprint, Name: k}
		cur, ok := bpByKey[k]
		e.Action = diffAction(ok, declDocsEqual(cur, d))
		plan.Entries = append(plan.Entries, e)
	}
	for _, b := range curBps {
		if k := bpKey(b.Name, b.Version); !declaredBp[k] {
			plan.Entries = append(plan.Entries, PlanEntry{Kind: KindBlueprint, Name: k, Action: ActionDelete})
		}
	}
	return plan, nil
}

// diffAction maps (exists, equal) to a plan action for a CaC-only kind.
func diffAction(exists, equal bool) Action {
	switch {
	case !exists:
		return ActionCreate
	case equal:
		return ActionNoop
	default:
		return ActionUpdate
	}
}

// Apply executes the plan for the declarations and returns the realized plan.
// Per-object failures are recorded on their entries; the rest still applies.
func Apply(ctx context.Context, store *graph.Store, decls Declarations) (Plan, error) {
	plan, err := ComputePlan(ctx, store, decls)
	if err != nil {
		return Plan{}, err
	}
	viewByName := map[string]types.ViewSelector{}
	for _, d := range decls.Views {
		viewByName[d.Name] = d.Selector
	}
	refByName := map[string]types.CredentialRef{}
	for _, d := range decls.CredentialRefs {
		refByName[d.Name] = d
	}
	connByName := map[string]types.Connector{}
	for _, d := range decls.Connectors {
		connByName[d.Name] = d
	}
	actByName := map[string]types.Actuator{}
	for _, d := range decls.Actuators {
		actByName[d.Name] = d
	}
	cbByName := map[string]types.CapabilityBinding{}
	for _, d := range decls.CapabilityBindings {
		cbByName[d.Name] = d
	}
	trigByName := map[string]types.Trigger{}
	for _, d := range decls.Triggers {
		trigByName[d.Name] = d
	}
	wfByName := map[string]types.Workflow{}
	for _, d := range decls.Workflows {
		wfByName[d.Name] = d
	}
	emByName := map[string]types.Emitter{}
	for _, d := range decls.Emitters {
		emByName[d.Name] = d
	}
	siteByName := map[string]types.Site{}
	for _, d := range decls.Sites {
		siteByName[d.Name] = d
	}
	cellByName := map[string]types.Cell{}
	for _, d := range decls.Cells {
		cellByName[d.Name] = d
	}
	scimByName := map[string]types.SCIMIdP{}
	for _, d := range decls.SCIMIdPs {
		scimByName[d.Name] = d
	}
	blByName := map[string]types.Baseline{}
	for _, d := range decls.Baselines {
		blByName[d.Name] = d
	}
	sinkByName := map[string]types.Sink{}
	for _, d := range decls.NotifySinks {
		sinkByName[d.Name] = d
	}
	subByName := map[string]types.Subscription{}
	for _, d := range decls.Subscriptions {
		subByName[d.Name] = d
	}
	msByName := map[string]types.MCPServer{}
	for _, d := range decls.MCPServers {
		msByName[d.Name] = d
	}
	// Keyed name@version to match PlanEntry.Name for KindIntent (ADR-0119 D1) — the same shape
	// bpByKey uses below. Keyed by name alone, every intent lookup here would miss.
	inByKey := map[string]types.Intent{}
	for _, d := range decls.Intents {
		inByKey[versionedRef(d.Name, d.Version)] = d
	}
	asgByName := map[string]types.Assignment{}
	for _, d := range decls.Assignments {
		asgByName[d.Name] = d
	}
	bpByKey := map[string]types.Blueprint{}
	for _, d := range decls.Blueprints {
		bpByKey[fmt.Sprintf("%s@%d", d.Name, d.Version)] = d
	}
	for i := range plan.Entries {
		e := &plan.Entries[i]
		if e.Action == ActionNoop {
			continue
		}
		// An entry that ComputePlan already REFUSED is not applied. Without this the pinned-version
		// guard (ADR-0119 D6) would be inert: it would attach an error to the entry and the very
		// next line would carry out the update anyway — a mechanism that reports a refusal while
		// performing the act. The refusal has to be the behaviour, not the message.
		//
		// Skipping rather than aborting the whole apply is deliberate: one poisoned document must
		// not stop every unrelated declaration from converging, and the error is already on the
		// entry for `stratt plan`/the reconcile log to surface (§1.8).
		if e.Error != "" {
			continue
		}
		var err error
		switch {
		case e.Kind == KindView && e.Action == ActionDelete:
			err = store.DeleteView(ctx, e.Name, graph.DeclaredByCaC)
		case e.Kind == KindView:
			_, err = store.DeclareViewAs(ctx, e.Name, viewByName[e.Name], graph.DeclaredByCaC)
		case e.Kind == KindCredentialRef && e.Action == ActionDelete:
			err = store.DeleteCredentialRef(ctx, e.Name, graph.DeclaredByCaC)
		case e.Kind == KindCredentialRef:
			_, err = store.DeclareCredentialRefAs(ctx, refByName[e.Name], graph.DeclaredByCaC)
		case e.Kind == KindConnector && e.Action == ActionDelete:
			err = store.DeleteConnector(ctx, e.Name)
		case e.Kind == KindConnector:
			err = store.UpsertConnector(ctx, connByName[e.Name])
		case e.Kind == KindActuator && e.Action == ActionDelete:
			err = store.DeleteActuator(ctx, e.Name)
		case e.Kind == KindActuator:
			err = store.UpsertActuator(ctx, actByName[e.Name])
		case e.Kind == KindCapabilityBinding && e.Action == ActionDelete:
			err = store.DeleteCapabilityBinding(ctx, e.Name)
		case e.Kind == KindCapabilityBinding:
			err = store.UpsertCapabilityBinding(ctx, cbByName[e.Name])
		case e.Kind == KindTrigger && e.Action == ActionDelete:
			err = store.DeleteTrigger(ctx, e.Name)
		case e.Kind == KindTrigger:
			err = store.UpsertTrigger(ctx, trigByName[e.Name])
		case e.Kind == KindWorkflow && e.Action == ActionDelete:
			err = store.DeleteWorkflow(ctx, e.Name)
		case e.Kind == KindWorkflow:
			err = store.UpsertWorkflow(ctx, wfByName[e.Name])
		case e.Kind == KindEmitter && e.Action == ActionDelete:
			err = store.DeleteEmitter(ctx, e.Name)
		case e.Kind == KindEmitter:
			err = store.UpsertEmitter(ctx, emByName[e.Name])
		case e.Kind == KindSite && e.Action == ActionDelete:
			err = store.DeleteSite(ctx, e.Name)
		case e.Kind == KindSite:
			err = store.UpsertSite(ctx, siteByName[e.Name])
		case e.Kind == KindCell && e.Action == ActionDelete:
			err = store.DeleteCell(ctx, e.Name)
		case e.Kind == KindCell:
			err = store.UpsertCell(ctx, cellByName[e.Name])
		case e.Kind == KindSCIMIdP && e.Action == ActionDelete:
			err = store.DeleteIDP(ctx, e.Name)
		case e.Kind == KindSCIMIdP:
			err = store.UpsertIDP(ctx, scimByName[e.Name])
		case e.Kind == KindNotifySink && e.Action == ActionDelete:
			err = store.DeleteNotifySink(ctx, e.Name)
		case e.Kind == KindNotifySink:
			err = store.UpsertNotifySink(ctx, sinkByName[e.Name])
		case e.Kind == KindSubscription && e.Action == ActionDelete:
			err = store.DeleteSubscription(ctx, e.Name)
		case e.Kind == KindSubscription:
			err = store.UpsertSubscription(ctx, subByName[e.Name])
		case e.Kind == KindBaseline && e.Action == ActionDelete:
			err = store.DeleteBaseline(ctx, e.Name)
		case e.Kind == KindBaseline:
			err = store.UpsertBaseline(ctx, blByName[e.Name])
		case e.Kind == KindMCPServer && e.Action == ActionDelete:
			err = store.DeleteMCPServer(ctx, e.Name)
		case e.Kind == KindMCPServer:
			err = store.UpsertMCPServer(ctx, msByName[e.Name])
		case e.Kind == KindIntent && e.Action == ActionDelete:
			// e.Name is name@version (ADR-0119 D1); split it so the version reaches the store,
			// mirroring the Blueprint delete case below.
			name, version, perr := splitVersionedRef("intent", e.Name)
			if perr != nil {
				err = perr
			} else {
				err = store.DeleteIntent(ctx, name, version)
			}
		case e.Kind == KindIntent:
			err = store.UpsertIntent(ctx, inByKey[e.Name])
		case e.Kind == KindAssignment && e.Action == ActionDelete:
			err = store.DeleteAssignment(ctx, e.Name)
		case e.Kind == KindAssignment:
			err = store.UpsertAssignment(ctx, asgByName[e.Name])
		case e.Kind == KindBlueprint && e.Action == ActionDelete:
			name, version, perr := splitBlueprintRef(e.Name)
			if perr != nil {
				err = perr
			} else {
				err = store.DeleteBlueprint(ctx, name, version)
			}
		case e.Kind == KindBlueprint:
			err = store.UpsertBlueprint(ctx, bpByKey[e.Name])
		}
		if err != nil {
			e.Error = err.Error()
		}
	}
	return plan, nil
}

// credentialRefsEqual compares pointer documents semantically.
func credentialRefsEqual(a, b types.CredentialRef) bool {
	a.DeclaredBy, b.DeclaredBy = "", ""
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	var va, vb any
	if json.Unmarshal(ja, &va) != nil || json.Unmarshal(jb, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// triggersEqual compares declaration documents semantically.
func triggersEqual(a, b types.Trigger) bool { return declDocsEqual(a, b) }

// declDocsEqual compares two declaration documents by canonical JSON.
func declDocsEqual(a, b any) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	var va, vb any
	if json.Unmarshal(ja, &va) != nil || json.Unmarshal(jb, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

func ptr(sel types.ViewSelector) *types.ViewSelector { return &sel }

func existsIn(m map[string]types.View, name string) bool { _, ok := m[name]; return ok }

// selectorsEqual compares selectors semantically: both sides round-trip
// through JSON so raw-message formatting and jsonb normalization differences
// don't read as drift.
func selectorsEqual(a, b types.ViewSelector) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	var va, vb any
	if json.Unmarshal(ja, &va) != nil || json.Unmarshal(jb, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// actuatorIdentities indexes declared Actuators by name → pluginIdentity. Only
// declarations that state an identity are indexed; a boot-registered Actuator is
// absent, and its name IS its identity, so name-based resolution stays right for it.
func actuatorIdentities(as []types.Actuator) map[string]string {
	if len(as) == 0 {
		return nil
	}
	m := make(map[string]string, len(as))
	for _, a := range as {
		if a.PluginIdentity != "" {
			m[a.Name] = a.PluginIdentity
		}
	}
	return m
}
