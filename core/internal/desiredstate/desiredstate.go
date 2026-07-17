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
	"sort"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/graph"
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
	Triggers       []types.Trigger       `json:"triggers"`
	Workflows      []types.Workflow      `json:"workflows"`
	Emitters       []types.Emitter       `json:"emitters"`
	Baselines      []types.Baseline      `json:"baselines"`
	MCPServers     []types.MCPServer     `json:"mcpServers"`
	Intents        []types.Intent        `json:"intents"`
	Assignments    []types.Assignment    `json:"assignments"`
	Blueprints     []types.Blueprint     `json:"blueprints"`
	NotifySinks    []types.Sink          `json:"notifySinks"`
	Subscriptions  []types.Subscription  `json:"subscriptions"`
	Sites          []types.Site          `json:"sites"`
	Cells          []types.Cell          `json:"cells"`
	SCIMIdPs       []types.SCIMIdP       `json:"scimIdps"`
}

// Declared kinds appearing in plans.
const (
	KindView          = "view"
	KindCredentialRef = "credential-ref"
	KindTrigger       = "trigger"
	KindWorkflow      = "workflow"
	KindEmitter       = "emitter"
	KindBaseline      = "baseline"
	KindMCPServer     = "mcp-server"
	KindIntent        = "intent"
	KindAssignment    = "assignment"
	KindBlueprint     = "blueprint"
	KindNotifySink    = "notify-sink"
	KindSubscription  = "subscription"
	KindSite          = "site"
	KindCell          = "cell"
	KindSCIMIdP       = "scim-idp"
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
func ParseDir(root string) (Declarations, error) {
	var out Declarations

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

	triggers, err := parseKind(filepath.Join(root, "triggers"), true, parseTriggerFile)
	if err != nil {
		return out, err
	}
	out.Triggers = triggers
	sort.Slice(out.Triggers, func(i, j int) bool { return out.Triggers[i].Name < out.Triggers[j].Name })

	workflows, err := parseKind(filepath.Join(root, "workflows"), true, parseWorkflowFile)
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

	baselines, err := parseKind(filepath.Join(root, "baselines"), true, parseBaselineFile)
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
	sort.Slice(out.Intents, func(i, j int) bool { return out.Intents[i].Name < out.Intents[j].Name })

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
	return out, nil
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
	if len(f.Injection) == 0 {
		return ref, fmt.Errorf("credential ref %s: injection policy is required", f.Name)
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
		Locator: locator, Injection: inj,
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
	Slices          int            `yaml:"slices"`
	CredentialRefs  []string       `yaml:"credentialRefs"`
	Principal       string         `yaml:"principal"`
	WorkflowName    string         `yaml:"workflowName"`
	FacetWriteScope []string       `yaml:"facetWriteScope"`
}

func parseTriggerFile(path string, raw []byte) (string, types.Trigger, error) {
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
		WorkflowName: f.WorkflowName, FacetWriteScope: f.FacetWriteScope,
	}
	if err := ValidateTrigger(t); err != nil {
		return "", types.Trigger{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return t.Name, t, nil
}

// ValidateTrigger checks one Trigger declaration; exported because the API's
// desired-state plan/apply path (the CLI applying the same Git checkout)
// validates the identical document shape.
func ValidateTrigger(t types.Trigger) error {
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
		return fmt.Errorf("trigger %s: workflowName launches carry no Step fields (the Workflow declares its own)", t.Name)
	}
	// Template namespace scope (ADR-0024): a Trigger's params/viewParams may
	// bind {{.event.x}} only on event-kind Triggers (a schedule fire has no
	// event); the spec/param namespaces belong to the compiler and the View,
	// not here.
	allowed := map[string]bool{}
	if t.Kind == types.TriggerEvent {
		allowed["event"] = true
	}
	if err := checkTemplateNamespaces("trigger "+t.Name, allowed, t.Params, t.ViewParams); err != nil {
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
		if err := validateParamsContract(t.Actuator, t.Params); err != nil {
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
		Method       string `yaml:"method"`
		BodyTemplate string `yaml:"bodyTemplate"`
		Endpoint     string `yaml:"endpoint"`
		Index        string `yaml:"index"`
		Facility     int    `yaml:"facility"`
		Insecure     bool   `yaml:"insecure"`
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
			Method: f.Config.Method, BodyTemplate: f.Config.BodyTemplate,
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
	if s.Kind != types.SinkWebhook {
		return fmt.Errorf("sink %s: unknown kind %q (webhook, splunk-hec, syslog, otel-logs)", s.Name, s.Kind)
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
	Framework           string         `yaml:"framework"`
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

func parseBaselineFile(path string, raw []byte) (string, types.Baseline, error) {
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
		RemediationWorkflow: f.RemediationWorkflow, Framework: f.Framework,
		Mode: f.Mode, FacetWriteScope: f.FacetWriteScope,
	}
	for _, ef := range f.Expected {
		exp, err := ef.toExpectation()
		if err != nil {
			return "", types.Baseline{}, fmt.Errorf("desiredstate: %s: expected: %w", path, err)
		}
		b.Expected = append(b.Expected, exp)
	}
	if err := ValidateBaseline(b); err != nil {
		return "", types.Baseline{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return b.Name, b, nil
}

// ValidateBaseline checks one Baseline declaration (ADR-0019). The check
// must be read-only by construction: only Actuators with check semantics are
// accepted, opentofu is pinned to plan mode, and ansible's check flag is the
// platform's to set — a declaration cannot even ask for a mutating check.
func ValidateBaseline(b types.Baseline) error {
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
		if len(b.Expected) == 0 {
			return fmt.Errorf("baseline %s: facet-observation requires at least one expected value", b.Name)
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
	if err := validateParamsContract(b.Actuator, b.Params); err != nil {
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
	Name     string         `yaml:"name"`
	Kind     string         `yaml:"kind"`
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
	in := types.Intent{Name: f.Name, Kind: f.Kind, Spec: f.Spec, OnRemove: f.OnRemove}
	if err := ValidateIntent(in); err != nil {
		return "", types.Intent{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return in.Name, in, nil
}

// ValidateIntent checks one Intent declaration (ADR-0023, ADR-0030). The kind
// is "implemented" iff it has a registered spec schema; the spec is validated
// at its seam (§1.1) and onRemove is gated per-kind (schema-driven removal
// semantics live in the kind, not tribal memory — §2.4).
func ValidateIntent(in types.Intent) error {
	if in.Name == "" {
		return fmt.Errorf("intent requires a name")
	}
	specRaw, err := json.Marshal(in.Spec)
	if err != nil {
		return fmt.Errorf("intent %s: marshal spec: %w", in.Name, err)
	}
	covered, err := contract.ValidateIntentSpec(in.Kind, specRaw)
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
	Name         string   `yaml:"name"`
	Intent       string   `yaml:"intent"`
	View         string   `yaml:"view"`
	Blueprint    string   `yaml:"blueprint"`
	Environments []string `yaml:"environments"`
	MaxDelta     *float64 `yaml:"maxDelta"`
	AckDelta     int      `yaml:"ackDelta"`
}

func parseAssignmentFile(path string, raw []byte) (string, types.Assignment, error) {
	var f assignmentFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.Assignment{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	name, version, err := splitBlueprintRef(f.Blueprint)
	if err != nil {
		return "", types.Assignment{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	a := types.Assignment{
		Name: f.Name, Intent: f.Intent, View: f.View,
		Blueprint: name, BlueprintVersion: version,
		Environments: f.Environments, MaxDelta: f.MaxDelta, AckDelta: f.AckDelta,
	}
	if err := ValidateAssignment(a); err != nil {
		return "", types.Assignment{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return a.Name, a, nil
}

// splitBlueprintRef parses "name@version".
func splitBlueprintRef(ref string) (string, int, error) {
	at := strings.LastIndex(ref, "@")
	if at <= 0 || at == len(ref)-1 {
		return "", 0, fmt.Errorf("blueprint must be name@version, got %q", ref)
	}
	version, err := strconv.Atoi(ref[at+1:])
	if err != nil || version < 1 {
		return "", 0, fmt.Errorf("blueprint version must be a positive integer, got %q", ref[at+1:])
	}
	return ref[:at], version, nil
}

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
	if a.MaxDelta != nil && (*a.MaxDelta <= 0 || *a.MaxDelta > 1) {
		return fmt.Errorf("assignment %s: maxDelta must be in (0, 1]", a.Name)
	}
	if a.AckDelta < 0 {
		return fmt.Errorf("assignment %s: ackDelta must be >= 0", a.Name)
	}
	return nil
}

// blueprintFile is the blueprints/*.yaml shape.
type blueprintFile struct {
	Name                string           `yaml:"name"`
	Version             int              `yaml:"version"`
	For                 string           `yaml:"for"`
	Routes              []blueprintRoute `yaml:"routes"`
	Severity            string           `yaml:"severity"`
	DampingObservations int              `yaml:"dampingObservations"`
	RemoveWorkflow      string           `yaml:"removeWorkflow"`
}
type blueprintRoute struct {
	Match               []declFacetPred `yaml:"match"`
	Observe             declExpectation `yaml:"observe"`
	Claim               string          `yaml:"claim"`
	RemediationWorkflow string          `yaml:"remediationWorkflow"`
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
		Severity: f.Severity, DampingObservations: f.DampingObservations,
		RemoveWorkflow: f.RemoveWorkflow,
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
func validateParamsContract(actuator string, params map[string]any) error {
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
	name := actuator
	raw := json.RawMessage(`{}`)
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("params: %w", err)
		}
		raw = b
	}
	return contract.ValidateActuatorParams(name, raw)
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
}
type stepYAML struct {
	Name            string         `yaml:"name"`
	Needs           []string       `yaml:"needs"`
	When            string         `yaml:"when"`
	Gate            *gateYAML      `yaml:"gate"`
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
}

func parseWorkflowFile(path string, raw []byte) (string, types.Workflow, error) {
	var f workflowFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.Workflow{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	w := types.Workflow{Name: f.Name}
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
			}
		}
		w.Steps = append(w.Steps, step)
	}
	if err := ValidateWorkflow(w); err != nil {
		return "", types.Workflow{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return w.Name, w, nil
}

// ValidateWorkflow checks one Workflow declaration; exported for the API's
// desired-state plan/apply path (same document shape as the Git checkout).
func ValidateWorkflow(w types.Workflow) error {
	if w.Name == "" || len(w.Steps) == 0 {
		return fmt.Errorf("workflow requires name and at least one step")
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
		// A Step is exactly one of three shapes (§2.3, ADR-0031): a Gate, an
		// Action (targetless typed operation), or an Actuation (Actuator+View).
		isGate := s.Gate != nil
		isAction := s.Action != ""
		isActuation := s.ViewName != "" || s.Actuator != "" || s.Slices != 0
		switch {
		case isGate && (isAction || isActuation):
			return fmt.Errorf("workflow %s: step %s: a step is a gate, an action, or an actuation — not multiple", w.Name, s.Name)
		case isAction && isActuation:
			return fmt.Errorf("workflow %s: step %s: a step is an action or an actuation, not both (actions are targetless — no viewName/actuator/slices)", w.Name, s.Name)
		case !isGate && !isAction && s.ViewName == "":
			return fmt.Errorf("workflow %s: step %s: actuation step requires viewName", w.Name, s.Name)
		case isGate && len(s.Gate.Approvers.Principals) == 0 && len(s.Gate.Approvers.Teams) == 0:
			return fmt.Errorf("workflow %s: step %s: gate requires approvers (principals and/or teams)", w.Name, s.Name)
		case isGate && s.Gate.TimeoutSeconds < 0:
			return fmt.Errorf("workflow %s: step %s: gate timeoutSeconds must be >= 0", w.Name, s.Name)
		case !isGate && s.Slices < 0:
			return fmt.Errorf("workflow %s: step %s: slices must be >= 0", w.Name, s.Name)
		}
		// Params/CredentialRefs may bind the event namespace (firing Emitter,
		// ADR-0024) and the steps namespace (a prior Step's outputs, ADR-0031),
		// both resolved at launch by ResolveStepParams.
		bindable := map[string]bool{"event": true, "steps": true}
		switch {
		case isAction:
			if err := validateActionParamsContract(s.Action, s.Params); err != nil {
				return fmt.Errorf("workflow %s: step %s: %w", w.Name, s.Name, err)
			}
			if err := checkTemplateNamespaces(
				fmt.Sprintf("workflow %s step %s", w.Name, s.Name), bindable, s.Params); err != nil {
				return err
			}
		case !isGate:
			if err := validateParamsContract(s.Actuator, s.Params); err != nil {
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
	return sel, nil
}

// ── plan / apply ─────────────────────────────────────────────────────────────

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
	inByName := map[string]types.Intent{}
	for _, in := range curIntents {
		inByName[in.Name] = in
	}
	declaredIn := map[string]bool{}
	for _, d := range decls.Intents {
		declaredIn[d.Name] = true
		e := PlanEntry{Kind: KindIntent, Name: d.Name}
		cur, ok := inByName[d.Name]
		e.Action = diffAction(ok, declDocsEqual(cur, d))
		plan.Entries = append(plan.Entries, e)
	}
	for _, in := range curIntents {
		if !declaredIn[in.Name] {
			plan.Entries = append(plan.Entries, PlanEntry{Kind: KindIntent, Name: in.Name, Action: ActionDelete})
		}
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
	inByName := map[string]types.Intent{}
	for _, d := range decls.Intents {
		inByName[d.Name] = d
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
			err = store.DeleteIntent(ctx, e.Name)
		case e.Kind == KindIntent:
			err = store.UpsertIntent(ctx, inByName[e.Name])
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
