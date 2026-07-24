package types

// Capability classes (ADR-0104) — the core-owned, versioned vocabulary a plugin may declare it
// `provides`, and a Connector/Actuator may declare it `requires`. A capability is a sovereign
// CONTRACT (ADR-0046 reserved set + ADR-0100 KeyCustodian): a dependency targets the class, never
// a named provider (§1.5), so the provider stays a swappable transport.
//
// The set is core-owned on purpose: a plugin never mints a capability's MEANING (§1.5) — it only
// advertises that it provides one of these. Resolution reads `provides` as governed CaC
// (operator-declared, store-visible on every replica — "the Manifest is advertisement; the grant
// is truth"), never the plugin's runtime self-claim.
//
// DurableExec is DELIBERATELY ABSENT (ADR-0104 D6): durable execution is load-bearing §1.4 spine
// (Temporal), an ambient platform guarantee — never a requirable, swappable capability. Likewise
// the core's own KeyCustodian consumption stays on ADR-0100's portCustodian (D7); `keycustodian`
// here is for the plugin→plugin edge.
// Each class is a PROVIDER-AGNOSTIC contract (§1.5, ADR-0105): the provider is a swappable
// transport, never baked into the class. Vendors named below are example provider #1s, never
// "the" provider — S3 vs Artifactory/GCS/Garage is an operator choice, not a code change.
const (
	CapKeyCustodian  = "keycustodian"  // wrap/unwrap a DEK in a KMS (provider #1: OpenBao Transit, ADR-0100)
	CapSecretBroker  = "secretbroker"  // resolve CredentialRef material at the SoR (e.g. OpenBao KV, ADR-0094)
	CapCertIssuer    = "certissuer"    // issue/renew certificates (e.g. OpenBao PKI, ADR-0098)
	CapStateStore    = "statestore"    // durable tool state, e.g. tofu remote state (provider #1: S3, ADR-0105)
	CapArtifactStore = "artifactstore" // content-addressed artifacts/evidence (provider #1: S3, ADR-0105)
	CapEventBus      = "eventbus"      // an estate-facing alternate event backend (reserved, ADR-0046)
	CapProvisioning  = "provisioning"  // provision machines other plugins target (e.g. EC2)
	CapIPAM          = "ipam"          // allocate a prefix/VLAN from a global IPAM (provider #1: NetBox, ADR-0111)
)

// capabilityClasses is the closed set the validator admits. Extending it is a core decision
// (a new class ships with its first provider — ADR-0104), never a plugin's to invent.
var capabilityClasses = map[string]bool{
	CapKeyCustodian:  true,
	CapSecretBroker:  true,
	CapCertIssuer:    true,
	CapStateStore:    true,
	CapArtifactStore: true,
	CapEventBus:      true,
	CapProvisioning:  true,
	CapIPAM:          true,
}

// ValidCapability reports whether tok is a known capability class (§1.5 — a plugin never mints a
// capability's meaning). Tokens are lowercase single words, matching the Manifest wire convention
// (proto capabilities, e.g. "keycustodian").
func ValidCapability(tok string) bool { return capabilityClasses[tok] }
