package types

// Connector capability classes (charter §2.2: a Connector packages Syncer/Action/Emitter).
// Emitter is RESERVED but not yet accepted — ADR-0103 defers Emitter Connectors.
const (
	ConnectorSyncer = "syncer"
	ConnectorAction = "action"
)

// Connector is the versioned integration package that BINDS a Source (charter §2.2 —
// Syncer/Action/Emitter). It is the operator-declared (Config-as-Code) authority for one
// plugin's Source binding + ownership grant: the desired half of a pluginhost.Grant plus the
// dial Address of the plugin's sovereign-port endpoint. Reconciled at runtime by the
// Connector registry (ADR-0103) — declared → dialed + registered, no strattd restart.
//
// It is a DISTINCT Named Kind from Actuator (§2.3): a Connector binds a Source; an Actuator
// runs tool content and binds none. It owns ONLY the desired half of its Source
// (Kind/Name/Endpoint/CredentialRef); the runtime placement fields (Cell/HomeEpoch/
// RehomingTo) are the home-gate single writer's domain (§2.4) and are REJECTED by
// ValidateConnector — a Connector must never set homing.
type Connector struct {
	// Name is the stable reference — the dispatch name and the authz object connector:<name>.
	Name string `json:"name"`
	// Class is the Connector capability: "syncer" or "action" (emitter reserved, ADR-0103).
	Class string `json:"class"`
	// Address is the plugin's sovereign-port gRPC endpoint the core dials (was
	// STRATT_<NAME>_PLUGIN_ADDR). Distinct from Source.Endpoint (the external SoR locator).
	Address string `json:"address"`
	// PluginIdentity is the authenticated channel identity; the Manifest's plugin_id must
	// equal it or registration fails (anti-spoof, ADR-0046).
	PluginIdentity string `json:"pluginIdentity"`
	// Tier is the trust tier ("community" | "trusted"); gates cross-source identity emission.
	Tier string `json:"tier,omitempty"`
	// Source is the operator-declared binding — DESIRED HALF ONLY (Kind/Name/Endpoint/
	// CredentialRef). Cell/HomeEpoch/RehomingTo MUST be empty (ValidateConnector rejects them).
	Source Source `json:"source"`
	// Ownership allowlists (§2.5): the Facet namespaces / label keys / identity schemes the
	// core registers ownership from and gates the plugin's emissions against.
	FacetNamespaces              []string `json:"facetNamespaces,omitempty"`
	AuthoritativeFacetNamespaces []string `json:"authoritativeFacetNamespaces,omitempty"`
	LabelKeys                    []string `json:"labelKeys,omitempty"`
	IdentitySchemes              []string `json:"identitySchemes,omitempty"`
	TombstoneSchemes             []string `json:"tombstoneSchemes,omitempty"`
	// EmitterName is the grant-bound emitter name the plugin may publish under (empty ⇒ the
	// Source name).
	EmitterName string `json:"emitterName,omitempty"`
	// ActionNames are the Connector Actions this plugin provides (namespaced, e.g.
	// "aws/create-vm"). A Connector's Action capability registers every-replica (ADR-0103 D3).
	ActionNames []string `json:"actionNames,omitempty"`
	// IntervalSeconds is the Syncer's Observe cadence (Class "syncer"); 0 ⇒ a sane default.
	IntervalSeconds int `json:"intervalSeconds,omitempty"`
	// Environments scopes this Connector to a subset of dev/staging/prod (ADR-0057); empty ⇒
	// every environment.
	Environments []string `json:"environments,omitempty"`
}
