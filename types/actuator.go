package types

// Actuator is an execution-engine plugin that runs TOOL CONTENT (helm, opentofu, ansible,
// script, mcp) — charter §2.3. It is a DISTINCT, permanent Named Kind from Connector (§2.2):
// it binds NO Source and holds NO facet/label ownership. It is the operator-declared
// (Config-as-Code) authority for one such plugin, reconciled at runtime by the registry
// (ADR-0103): declared → dialed + registered into the Actuator/Action dispatch table on
// every replica, no strattd restart.
//
// (This is the CaC DECLARATION Kind for a plugin Actuator — orthogonal to the in-tree
// actuators.Actuator interface and the orchestrate.PluginActuator runtime dispatch entry.)
type Actuator struct {
	// Name is the stable Actuator reference — the dispatch name and the authz object
	// actuator:<name>.
	Name string `json:"name"`
	// Address is the plugin's sovereign-port gRPC endpoint the core dials (long-lived gRPC
	// transport). Empty for an EE-Job (subprocess) Actuator, which sets JobCommand instead.
	Address string `json:"address,omitempty"`
	// PluginIdentity is the authenticated channel identity (anti-spoof; the govern grant's id).
	PluginIdentity string `json:"pluginIdentity"`
	// Tier is the trust tier ("community" | "trusted").
	Tier string `json:"tier,omitempty"`
	// DryRunnable declares the Actuator supports a side-effect-free plan/dry-run (reconciled
	// from the Manifest at registration, never trusted live).
	DryRunnable bool `json:"dryRunnable,omitempty"`
	// ActionNames are the targetless Connector Actions this Actuator also exposes (e.g. helm's
	// "helm/deploy" — ADR-0092 dual surface); registered into the Action dispatch table.
	ActionNames []string `json:"actionNames,omitempty"`
	// JobCommand, when set, marks the EE-Job (subprocess) transport (ADR-0051): the core
	// dispatches a K8s Job whose entrypoint is this command instead of a long-lived gRPC Apply.
	JobCommand []string `json:"jobCommand,omitempty"`
	// Image overrides the dispatcher's default EE image for this Actuator's Jobs (ADR-0053:
	// mcp needs the python-bearing EE-mcp image).
	Image string `json:"image,omitempty"`
	// MCP marks the mcp EE-Job transport (ADR-0053).
	MCP bool `json:"mcp,omitempty"`
	// Provides are the capability classes this Actuator fulfils (ADR-0104) — governed CaC
	// provision, store-visible on every replica (§1.5). Each token must be a known
	// types.ValidCapability.
	Provides []string `json:"provides,omitempty"`
	// Requires are the capability classes this Actuator depends on (ADR-0104): it is withheld from
	// the dispatch table (registry D6 PENDING status) until a provider for each is declared. A
	// dependency on the CONTRACT, never a named provider (§1.5); a gate, never a precedence (§2.4).
	Requires []string `json:"requires,omitempty"`
	// Environments scopes this Actuator (ADR-0057); empty ⇒ every environment.
	Environments []string `json:"environments,omitempty"`
}
