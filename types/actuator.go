package types

// Actuator is an execution-engine plugin that runs TOOL CONTENT (helm, opentofu, ansible,
// script, mcp) — charter §2.3. It is a DISTINCT, permanent Named Kind from Connector (§2.2):
// it binds NO Source and holds NO facet/label ownership — registering an Actuator never
// claims a facet OWNER row, so the §2.1 one-writer-per-namespace rule is neither tripped
// nor bypassed. FacetNamespaces below is a write CEILING for a Run's write-back (ANDed with
// the Step's own scope), not an ownership claim; the write itself is stamped with Run
// provenance, which §1.2 permits. It is the operator-declared
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
	// mcp needs the python-bearing EE-mcp image; ADR-0117 D3a: it is ALSO how per-Step
	// ansible content is selected — two declarations differing only in their EE image,
	// so the spine never reads a tool's params to pick an image).
	Image string `json:"image,omitempty"`
	// FacetNamespaces are the Facet namespaces this Actuator's write-back may touch — the
	// MF3 BOUNDED grant, declared as CaC exactly as a Connector declares its own (ADR-0103).
	// An Actuator owns no Source and does not sync, but a tool-content Run can still report
	// OBSERVED state back (ansible's fact-back convention, ADR-0084), and that write must be
	// gated by an explicit, reviewable grant rather than a wildcard. Empty ⇒ this Actuator
	// may write NO facet, which is the correct default for a pure executor.
	//
	// Declaring it here is what makes a CaC-declared EE-Job Actuator equivalent to a
	// boot-registered one: without it, a declared ansible variant would run but have every
	// fact write-back refused — a strictly weaker Actuator that looks identical (§1.8).
	FacetNamespaces []string `json:"facetNamespaces,omitempty"`
	// IdentitySchemes are the identity schemes this Actuator may correlate write-back by
	// (ansible correlates facts by host.name). Same CaC-grant rationale as FacetNamespaces.
	IdentitySchemes []string `json:"identitySchemes,omitempty"`
	// ElevatedInputs are dotted paths into this Actuator's Step params whose presence (truthy)
	// means the Step ELEVATES PRIVILEGE on its target — `become.enabled` for ansible
	// (ADR-0122 D3). Core walks them and derives the `stratt.change/privileged` change-context
	// label, which a Control can then gate on.
	//
	// This is what closes ADR-0117 D1's honest gap: typed `become` was declared and audited but
	// not Control-gateable, because ChangeContext carries no Step params and teaching the PDP to
	// read inside an ansible field is the `if ansible{}` §1.4 forbids. Core reads a declared path
	// list and a boolean and never learns the word `become` — the same content-blind shape as
	// FacetNamespaces above, where core enforces a ceiling without knowing what a namespace means.
	//
	// It lives on the Actuator rather than in the tool's input Contract, which would be the better
	// long-run home: `ansible.input.v5` is pinned and hash-verified (§1.5), so annotating it in
	// place is blocking drift, and minting v6 pulls in ADR-0117 D2's deferred removal of the
	// deprecated `check`/`eeImage` fields. Reviewed in Git here, beside the other CaC grants it
	// resembles; it moves to the Contract whenever a v6 is minted for its own reasons.
	//
	// Empty ⇒ this Actuator elevates nothing as far as core can tell. Honest and visible in
	// review, but not automatic: an Actuator that omits it derives no label.
	ElevatedInputs []string `json:"elevatedInputs,omitempty"`
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
	// Provisions maps an Intent kind (no "Intent/" prefix — "Compute", "Subnet", …) to THIS
	// provider's build Action for it (ADR-0110 D3), meaningful only when this Actuator
	// `provides` provisioning. It is how a `provisioning` provider advertises its per-kind build
	// mechanism so an Intent's `requires: [provisioning]` resolves to a concrete Action — the
	// provider owns its mechanism (§1.5); a capability-binding only selects WHICH provider.
	Provisions map[string]string `json:"provisions,omitempty"`
	// Decommissions maps an Intent kind to THIS provider's gated TEARDOWN Workflow for it (ADR-0114
	// D4) — the symmetric counterpart to Provisions. It is how a provisioning provider advertises its
	// per-kind teardown so a withdrawn/counted-down Intent (onRemove: remove) resolves to a concrete
	// gated teardown; the provider owns its mechanism (§1.5). Meaningful only when this Actuator
	// `provides` provisioning.
	Decommissions map[string]string `json:"decommissions,omitempty"`
	// Environments scopes this Actuator (ADR-0057); empty ⇒ every environment.
	Environments []string `json:"environments,omitempty"`
}
