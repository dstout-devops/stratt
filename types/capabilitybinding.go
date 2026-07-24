package types

// CapabilityBinding is the operator-declared (Config-as-Code) selection of WHICH verified
// provider fulfils a capability class for a given Intent kind (ADR-0110 D3). It is a CaC
// DECLARATION FORM the capability registry reconciles — deliberately NOT a new Named Kind
// (§2 vocabulary is frozen v1.0); it is estate configuration, exactly as estate/authz/ tuples
// configure OpenFGA without being a Kind. It is the first materialization of the
// capability→provider binding surface booked by ADR-0104 D3 / ADR-0105 D5, extended here from
// (class → provider) to (class, provider, Intent kind → build Action) for the enablement-gate
// provisioning reach-path (ADR-0110).
//
// Provider selection is a landscape choice (§1.5), never the Intent author's: an Intent declares
// `requires: [provisioning]` (the class); this binding names the concrete provider + build Action.
// Resolution (ADR-0110 D3/D4) auto-binds the SOLE verified provider of a class for a kind, so a
// binding is REQUIRED only to disambiguate >1 — and >1 with no binding is a compile error (§2.4),
// never a silent tiebreak.
type CapabilityBinding struct {
	// Name is the stable declaration reference (the store PK + reconcile identity).
	Name string `json:"name"`
	// Entries are the (capability, provider, Intent kind → build Action) selections this document
	// declares. One document may carry many (e.g. every provisioning kind for one environment).
	Entries []BindingEntry `json:"entries"`
	// Environments scopes this binding (ADR-0057); empty ⇒ every environment.
	Environments []string `json:"environments,omitempty"`
}

// BindingEntry is one capability→provider selection for a specific Intent kind. It selects only the
// PROVIDER — the build Action is the provider's own to declare (Actuator/Connector `provisions`, per
// ADR-0110 D3: "the bound provider declares its build Action per Intent kind"). So a binding never
// re-specifies a provider's mechanism (§1.5); it disambiguates which provider when >1 could build.
type BindingEntry struct {
	// Capability is the class being bound — a known ValidCapability (e.g. "provisioning").
	Capability string `json:"capability"`
	// Provider is the verified provider's declaration name (an Actuator/Connector that `provides`
	// the class). Resolution checks it against the verified-provider index (ADR-0104 slice 2).
	Provider string `json:"provider"`
	// IntentKind is the Intent kind this entry routes, WITHOUT the "Intent/" prefix
	// (e.g. "Compute", "Subnet", "Vlan", "Dmz") — the reach-path is per kind (ADR-0110 D3).
	IntentKind string `json:"intentKind"`
}
