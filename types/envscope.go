package types

// EnvScoped is a declaration that MAY be scoped to a subset of environments
// (dev / staging / prod / ring — ADR-0057). Empty = all environments. It is a
// boolean MEMBERSHIP filter only — never a router, and never a source of
// env-conditional *values*: env-conditional config values would be a
// new-configuration-language non-goal. Per-environment values are declared by
// having ONE Assignment PER ENVIRONMENT, each carrying flat `values`
// (ADR-0118 D1) — the membership filter selects which Assignment applies, and the
// values are ordinary declarations inside it. A map keyed by environment name is
// forbidden and rejected at declaration (desiredstate.rejectEnvKeyedValues).
// Any future declaration kind that can independently launch a Run at the
// reconcile boundary MUST implement this, or it is an environment hole.
type EnvScoped interface {
	// ScopedEnvironments returns the environments this declaration belongs to;
	// empty means all environments.
	ScopedEnvironments() []string
}

func (a Assignment) ScopedEnvironments() []string { return a.Environments }
func (t Trigger) ScopedEnvironments() []string    { return t.Environments }
func (b Baseline) ScopedEnvironments() []string   { return b.Environments }

// An Intent joins the contract too, and it is the case the paragraph above did not cover.
// The rule there — "per-environment values are declared by having ONE Assignment PER
// ENVIRONMENT" — assumes every Intent HAS an Assignment. A provisioning Intent does not:
// ADR-0058 makes provisioning a sibling reconcile that selects an Intent BY NAME, so there
// was no scoped document anywhere in its path and it applied in every environment
// unconditionally. Same membership semantics, same prohibition on env-keyed values; the
// filter simply had to live on the Intent because for that family nothing else carries one.
func (i Intent) ScopedEnvironments() []string { return i.Environments }

// Provider-selection declarations are env-scoped too (ADR-0113 D2): the provisioning
// reach-path resolves a provider per environment, so an Actuator/Connector that
// `provides` a capability and the CapabilityBinding that selects it both join the
// EnvScoped contract — an environment is the substrate/sovereignty boundary.
func (a Actuator) ScopedEnvironments() []string          { return a.Environments }
func (c Connector) ScopedEnvironments() []string         { return c.Environments }
func (b CapabilityBinding) ScopedEnvironments() []string { return b.Environments }

// InScope reports whether a declaration tagged for `envs` is in scope for a
// daemon whose active environment is `active` (ADR-0057). An unscoped daemon
// (active == "") sees everything; an untagged declaration (envs empty) is in
// every environment; otherwise the active environment must be one of `envs`.
// This is membership, never precedence (§2.4) — no ordering, no last-writer-wins.
func InScope(envs []string, active string) bool {
	if active == "" || len(envs) == 0 {
		return true
	}
	for _, e := range envs {
		if e == active {
			return true
		}
	}
	return false
}
