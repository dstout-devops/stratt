package desiredstate

import (
	"context"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// Connector + Actuator desired-state Kinds (ADR-0103) — two peer Named Kinds (§2.2/§2.3),
// modeled on Trigger: CaC-only, the reconcile engine is sole writer, projected to
// graph.connector / graph.actuator, reconciled at runtime by the Connector registry.

// ── Connector (binds a Source; Syncer/Action) ───────────────────────────────

// sourceFile decodes the operator-declared Source binding. Homing fields (cell/rehomingTo/
// homeEpoch) are DECODED on purpose so ValidateConnector can reject them with a precise
// message — a Connector owns only the desired half of its Source (§2.4).
type sourceFile struct {
	Kind          string `yaml:"kind"`
	Name          string `yaml:"name"`
	Endpoint      string `yaml:"endpoint"`
	CredentialRef string `yaml:"credentialRef"`
	Cell          string `yaml:"cell"`
	RehomingTo    string `yaml:"rehomingTo"`
	HomeEpoch     int64  `yaml:"homeEpoch"`
}

type connectorFile struct {
	Name                         string     `yaml:"name"`
	Class                        string     `yaml:"class"`
	Address                      string     `yaml:"address"`
	PluginIdentity               string     `yaml:"pluginIdentity"`
	Tier                         string     `yaml:"tier"`
	Source                       sourceFile `yaml:"source"`
	FacetNamespaces              []string   `yaml:"facetNamespaces"`
	AuthoritativeFacetNamespaces []string   `yaml:"authoritativeFacetNamespaces"`
	LabelKeys                    []string   `yaml:"labelKeys"`
	IdentitySchemes              []string   `yaml:"identitySchemes"`
	TombstoneSchemes             []string   `yaml:"tombstoneSchemes"`
	EmitterName                  string     `yaml:"emitterName"`
	ActionNames                  []string   `yaml:"actionNames"`
	IntervalSeconds              int        `yaml:"intervalSeconds"`
	Provides                     []string   `yaml:"provides"`
	Requires                     []string   `yaml:"requires"`
	Environments                 []string   `yaml:"environments"`
}

func parseConnectorFile(path string, raw []byte) (string, types.Connector, error) {
	var f connectorFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // a typo in a declaration must fail, not silently vanish
	if err := dec.Decode(&f); err != nil {
		return "", types.Connector{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	c := types.Connector{
		Name: f.Name, Class: f.Class, Address: f.Address, PluginIdentity: f.PluginIdentity, Tier: f.Tier,
		Source: types.Source{
			Kind: f.Source.Kind, Name: f.Source.Name, Endpoint: f.Source.Endpoint, CredentialRef: f.Source.CredentialRef,
			Cell: f.Source.Cell, RehomingTo: f.Source.RehomingTo, HomeEpoch: f.Source.HomeEpoch,
		},
		FacetNamespaces: f.FacetNamespaces, AuthoritativeFacetNamespaces: f.AuthoritativeFacetNamespaces,
		LabelKeys: f.LabelKeys, IdentitySchemes: f.IdentitySchemes, TombstoneSchemes: f.TombstoneSchemes,
		EmitterName: f.EmitterName, ActionNames: f.ActionNames, IntervalSeconds: f.IntervalSeconds,
		Provides: f.Provides, Requires: f.Requires, Environments: f.Environments,
	}
	if err := ValidateConnector(c); err != nil {
		return "", types.Connector{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return c.Name, c, nil
}

// ValidateConnector enforces the Connector contract (ADR-0103): a bound Source (desired half
// ONLY — no runtime homing, §2.4), a valid capability Class, and the same allowlist-subset
// rules pluginhost.Grant enforces.
func ValidateConnector(c types.Connector) error {
	if c.Name == "" {
		return fmt.Errorf("connector: name is required")
	}
	if c.Address == "" {
		return fmt.Errorf("connector %q: address is required (the plugin's sovereign-port endpoint)", c.Name)
	}
	if c.PluginIdentity == "" {
		return fmt.Errorf("connector %q: pluginIdentity is required (anti-spoof)", c.Name)
	}
	switch c.Class {
	case types.ConnectorSyncer, types.ConnectorAction:
	case "":
		return fmt.Errorf("connector %q: class is required (syncer|action)", c.Name)
	default:
		return fmt.Errorf("connector %q: unknown class %q (syncer|action; emitter reserved, ADR-0103)", c.Name, c.Class)
	}
	if c.Source.Name == "" {
		return fmt.Errorf("connector %q: source.name is required (a Connector binds a Source, §2.2)", c.Name)
	}
	// §2.4: a Connector owns only the desired half of its Source; Cell/HomeEpoch/RehomingTo
	// are the home-gate reconciler's single-writer placement domain — never Config-as-Code.
	if c.Source.Cell != "" || c.Source.RehomingTo != "" || c.Source.HomeEpoch != 0 {
		return fmt.Errorf("connector %q: source must not set cell/homeEpoch/rehomingTo — runtime placement is not CaC (§2.4)", c.Name)
	}
	if !subsetOf(c.AuthoritativeFacetNamespaces, c.FacetNamespaces) {
		return fmt.Errorf("connector %q: authoritativeFacetNamespaces must be a subset of facetNamespaces (ADR-0060)", c.Name)
	}
	if !subsetOf(c.TombstoneSchemes, c.IdentitySchemes) {
		return fmt.Errorf("connector %q: tombstoneSchemes must be a subset of identitySchemes (ADR-0042)", c.Name)
	}
	if err := validateCapabilities(c.Name, c.Provides, c.Requires); err != nil {
		return err
	}
	return nil
}

// validateCapabilities enforces the ADR-0104 core-owned capability vocabulary (§1.5 — a plugin
// never mints a capability's meaning): every provides/requires token must be a known class.
func validateCapabilities(name string, provides, requires []string) error {
	for _, tok := range provides {
		if !types.ValidCapability(tok) {
			return fmt.Errorf("%q: unknown capability %q in provides (core-owned vocabulary, ADR-0104 §1.5)", name, tok)
		}
	}
	for _, tok := range requires {
		if !types.ValidCapability(tok) {
			return fmt.Errorf("%q: unknown capability %q in requires (core-owned vocabulary, ADR-0104 §1.5)", name, tok)
		}
	}
	return nil
}

func computeConnectorPlan(ctx context.Context, store *graph.Store, decls []types.Connector) (Plan, error) {
	current, err := store.ListConnectors(ctx)
	if err != nil {
		return Plan{}, err
	}
	byName := map[string]types.Connector{}
	for _, c := range current {
		byName[c.Name] = c
	}
	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindConnector, Name: d.Name}
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
			plan.Entries = append(plan.Entries, PlanEntry{Kind: KindConnector, Name: c.Name, Action: ActionDelete})
		}
	}
	return plan, nil
}

// ── Actuator (runs tool content; no Source) ─────────────────────────────────

type actuatorFile struct {
	Name           string   `yaml:"name"`
	Address        string   `yaml:"address"`
	PluginIdentity string   `yaml:"pluginIdentity"`
	Tier           string   `yaml:"tier"`
	DryRunnable    bool     `yaml:"dryRunnable"`
	ActionNames    []string `yaml:"actionNames"`
	JobCommand     []string `yaml:"jobCommand"`
	Image          string   `yaml:"image"`
	MCP            bool     `yaml:"mcp"`
	Provides       []string `yaml:"provides"`
	Requires       []string `yaml:"requires"`
	Environments   []string `yaml:"environments"`
}

func parseActuatorFile(path string, raw []byte) (string, types.Actuator, error) {
	var f actuatorFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return "", types.Actuator{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	a := types.Actuator{
		Name: f.Name, Address: f.Address, PluginIdentity: f.PluginIdentity, Tier: f.Tier,
		DryRunnable: f.DryRunnable, ActionNames: f.ActionNames, JobCommand: f.JobCommand,
		Image: f.Image, MCP: f.MCP, Provides: f.Provides, Requires: f.Requires, Environments: f.Environments,
	}
	if err := ValidateActuator(a); err != nil {
		return "", types.Actuator{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return a.Name, a, nil
}

// ValidateActuator enforces the Actuator contract (ADR-0103): no Source (an Actuator runs
// tool content, §2.3), and exactly one transport — a gRPC Address OR an EE-Job command.
func ValidateActuator(a types.Actuator) error {
	if a.Name == "" {
		return fmt.Errorf("actuator: name is required")
	}
	if a.PluginIdentity == "" {
		return fmt.Errorf("actuator %q: pluginIdentity is required (anti-spoof)", a.Name)
	}
	switch {
	case a.Address == "" && len(a.JobCommand) == 0:
		return fmt.Errorf("actuator %q: one of address (gRPC) or jobCommand (EE-Job) is required", a.Name)
	case a.Address != "" && len(a.JobCommand) > 0:
		return fmt.Errorf("actuator %q: address and jobCommand are mutually exclusive transports", a.Name)
	}
	if err := validateCapabilities(a.Name, a.Provides, a.Requires); err != nil {
		return err
	}
	return nil
}

func computeActuatorPlan(ctx context.Context, store *graph.Store, decls []types.Actuator) (Plan, error) {
	current, err := store.ListActuators(ctx)
	if err != nil {
		return Plan{}, err
	}
	byName := map[string]types.Actuator{}
	for _, a := range current {
		byName[a.Name] = a
	}
	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindActuator, Name: d.Name}
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
	for _, a := range current {
		if !declared[a.Name] {
			plan.Entries = append(plan.Entries, PlanEntry{Kind: KindActuator, Name: a.Name, Action: ActionDelete})
		}
	}
	return plan, nil
}

// subsetOf reports whether every element of sub is in super.
func subsetOf(sub, super []string) bool {
	set := make(map[string]bool, len(super))
	for _, s := range super {
		set[s] = true
	}
	for _, s := range sub {
		if !set[s] {
			return false
		}
	}
	return true
}
