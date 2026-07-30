package desiredstate

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"context"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// CapabilityBinding desired-state form (ADR-0110 D3) — NOT a Named Kind (§2 frozen), a CaC
// declaration the capability registry reconciles, modeled on Actuator: CaC-only, the reconcile
// engine is sole writer, projected to graph.capability_binding. It selects WHICH verified provider
// fulfils a capability class for a given Intent kind, so an Intent's `requires: [provisioning]`
// resolves to a concrete provider + build Action (resolution itself is ADR-0110 D4, a later slice).

type bindingEntryFile struct {
	Capability string `yaml:"capability"`
	Provider   string `yaml:"provider"`
	IntentKind string `yaml:"intentKind"`
	// Substrate selects by a declared provider property instead of by name (ADR-0151 D2).
	Substrate string `yaml:"substrate"`
}

type capabilityBindingFile struct {
	Name         string             `yaml:"name"`
	Entries      []bindingEntryFile `yaml:"entries"`
	Environments []string           `yaml:"environments"`
}

func parseCapabilityBindingFile(path string, raw []byte) (string, types.CapabilityBinding, error) {
	var f capabilityBindingFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // a typo in a declaration must fail, not silently vanish
	if err := dec.Decode(&f); err != nil {
		return "", types.CapabilityBinding{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	b := types.CapabilityBinding{Name: f.Name, Environments: f.Environments}
	for _, e := range f.Entries {
		b.Entries = append(b.Entries, types.BindingEntry{
			Capability: e.Capability, Provider: e.Provider, IntentKind: e.IntentKind,
			Substrate: e.Substrate,
		})
	}
	if err := ValidateCapabilityBinding(b); err != nil {
		return "", types.CapabilityBinding{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return b.Name, b, nil
}

// ValidateCapabilityBinding enforces the ADR-0110 binding contract: a named document with >=1
// entry, each naming a known capability class (§1.5 core-owned vocabulary), a provider, an Intent
// kind, and a build Action — with no duplicate (capability, Intent-kind) WITHIN the document (a
// within-document collision is an unambiguous authoring error; a cross-document collision in one
// scope is the §2.4 resolution-time compile error, ADR-0110 D3).
func ValidateCapabilityBinding(b types.CapabilityBinding) error {
	if b.Name == "" {
		return fmt.Errorf("capability-binding: name is required")
	}
	if len(b.Entries) == 0 {
		return fmt.Errorf("capability-binding %q: at least one entry is required", b.Name)
	}
	seen := map[string]bool{}
	for i, e := range b.Entries {
		if !types.ValidCapability(e.Capability) {
			return fmt.Errorf("capability-binding %q: entry %d: unknown capability %q (core-owned vocabulary, ADR-0104 §1.5)", b.Name, i, e.Capability)
		}
		// EXACTLY ONE selector (ADR-0151 D2). Both is refused rather than ranked: an entry naming
		// a provider AND a substrate answers "which provider" twice, and nothing good happens when
		// the two answers differ. Neither is refused because an entry that selects nothing is not
		// a binding.
		switch {
		case e.Provider == "" && e.Substrate == "":
			return fmt.Errorf("capability-binding %q: entry %d: one of provider or substrate is required — provider names a verified provider, substrate (ADR-0151 D2) selects by a property the providers declare", b.Name, i)
		case e.Provider != "" && e.Substrate != "":
			return fmt.Errorf("capability-binding %q: entry %d: provider %q and substrate %q are mutually exclusive — an entry selects a provider ONE way, or the two selectors can disagree (ADR-0151 D2)", b.Name, i, e.Provider, e.Substrate)
		case e.Substrate != "" && !types.ValidSubstrate(e.Substrate):
			return fmt.Errorf("capability-binding %q: entry %d: unknown substrate %q — the closed set is %v (ADR-0151 D1)", b.Name, i, e.Substrate, types.Substrates())
		}
		// intentKind is REQUIRED for a provider entry (the reach path is per kind) and OPTIONAL for
		// a substrate entry, whose whole point is to cover every kind at once.
		if e.Provider != "" && e.IntentKind == "" {
			return fmt.Errorf("capability-binding %q: entry %d: intentKind is required with provider (e.g. Compute, Subnet — no Intent/ prefix)", b.Name, i)
		}
		if short, ok := strings.CutPrefix(e.IntentKind, "Intent/"); ok {
			return fmt.Errorf("capability-binding %q: entry %d: intentKind %q must omit the Intent/ prefix (write %q)", b.Name, i, e.IntentKind, short)
		}
		// A substrate entry and a provider entry for the same kind are DIFFERENT keys, so both may
		// appear in one document — that is the deliberate override, not a collision.
		key := e.Capability + "\x00" + e.IntentKind + "\x00" + e.Substrate
		if seen[key] {
			return fmt.Errorf("capability-binding %q: entry %d: duplicate (capability %q, intentKind %q, substrate %q) in one document — a within-document collision (§2.4)", b.Name, i, e.Capability, e.IntentKind, e.Substrate)
		}
		seen[key] = true
	}
	return nil
}

func computeCapabilityBindingPlan(ctx context.Context, store *graph.Store, decls []types.CapabilityBinding) (Plan, error) {
	current, err := store.ListCapabilityBindings(ctx)
	if err != nil {
		return Plan{}, err
	}
	byName := map[string]types.CapabilityBinding{}
	for _, b := range current {
		byName[b.Name] = b
	}
	var plan Plan
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Name] = true
		entry := PlanEntry{Kind: KindCapabilityBinding, Name: d.Name}
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
	for _, b := range current {
		if !declared[b.Name] {
			plan.Entries = append(plan.Entries, PlanEntry{Kind: KindCapabilityBinding, Name: b.Name, Action: ActionDelete})
		}
	}
	return plan, nil
}
