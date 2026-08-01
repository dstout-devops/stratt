package types

// Intent kinds (charter §2.4). Intent/Application shipped in Phase 2;
// Intent/Certificate is the Phase-3 promote-flagship GA (§8, ADR-0030). The
// FileSet/Access kinds follow. Each kind has a registered spec schema
// (contracts/intents/<kind>.schema.json) — an Intent kind is "implemented"
// exactly when its schema exists (§1.1).
const (
	IntentApplication = "Intent/Application"
	IntentCertificate = "Intent/Certificate"
	// IntentFileSet distributes files to hosts (content-addressed, checksum
	// Facets); IntentAccess governs host/OS-level access (additive claims:
	// local admin groups, sudoers, authorized_keys) — ADR-0036. Neither is
	// platform authorization, which stays the CaC/OpenFGA spine (§2.5).
	IntentFileSet = "Intent/FileSet"
	IntentAccess  = "Intent/Access"
	// IntentCompute declares desired compute infrastructure (ADR-0058): a count
	// of instances that SHOULD exist. Unlike the other kinds it does not compile
	// to observation Baselines — a sibling provisioning reconcile compares its
	// count against projected+correlated Entities and surfaces GATED builds
	// (§5 Flow 1), never an Entity for the unbuilt (§1.2).
	IntentCompute = "Intent/Compute"
	// Network/topology provisioning Intents (ADR-0059): cardinality-1 NAMED
	// SINGLETONS (a subnet IS a CIDR, not a count). Like Intent/Compute they do not
	// compile to observation Baselines — the provisioning reconcile compares the one
	// named desired Entity against its correlated projection and surfaces a GATED
	// build (§5 Flow 1), never an Entity for the unbuilt (§1.2). The built infra
	// projects as a subnet/dns-record/dmz Entity kind (decision 1).
	IntentSubnet    = "Intent/Subnet"
	IntentDnsRecord = "Intent/DnsRecord"
	IntentDmz       = "Intent/Dmz"
	IntentVlan      = "Intent/Vlan"
)

// SingletonIntentKinds are the provisioning Intent kinds planned as cardinality-1
// named singletons (ADR-0059 decision 4), as opposed to Intent/Compute's count/ordinal
// fan-out. The provisioning reconcile branches on membership here.
var SingletonIntentKinds = map[string]bool{
	IntentSubnet:    true,
	IntentDnsRecord: true,
	IntentDmz:       true,
	IntentVlan:      true,
}

// AssignableIntentKind reports whether an Intent kind may carry a `version` (ADR-0119 D3).
//
// The rule is structural, not a list: a version is only meaningful where a SEAM EXISTS TO CARRY
// THE PIN. An Assignment selects application-shaped Intents, so it can pin one. Provisioning
// kinds are selected by NAME by the provisioning reconcile (ADR-0058 makes it a sibling loop with
// no Assignment), so there is nowhere for a pin to live — and two versions of one fleet are not
// two rings, they are two claims on the same machines, which ADR-0058 D5 already rejects as an
// exclusive-instance-identity collision.
//
// DERIVED from the kind constants on purpose. A literal list in the validator would rot: the
// ADR-0059 provisioning family is still growing (ADR-0110/0111/0112), and a new kind must inherit
// the refusal automatically rather than by someone remembering to add it here.
func AssignableIntentKind(kind string) bool {
	return kind != IntentCompute && !SingletonIntentKinds[kind]
}

// onRemove lifecycle values (charter §2.4): what happens to compiled state
// when the Intent (or its Assignment) is withdrawn. v1 implements `retain`
// (leave compiled state, raise an orphan Finding — never silent); `revert`
// and `remove` carry domain-specific removal semantics in the kind schema
// and land with the Phase-3 kinds.
const (
	OnRemoveRetain = "retain"
	OnRemoveRevert = "revert"
	OnRemoveRemove = "remove"
)

// Intent is a small declarative document of *what* (charter §2.4): the
// team-facing surface. It carries no targeting — an Assignment binds it to a
// View. CaC-only, like every desired-state declaration (§1.2).
type Intent struct {
	Name string `json:"name"`
	// Version is the CONFIGURATION version of this document (ADR-0119 D1), default 1. Together
	// with Name it is the storage identity, exactly as for Blueprint — so test, stage and prod
	// can run three configurations of one Intent simultaneously, each pinned by its own
	// environment-scoped Assignment.
	//
	// Identity-forming, like Blueprint.version: rows coexist. NOT the monotonic revision counter
	// View.version is (auto-bumped by a trigger, one row per name), and NOT the schema-SHAPE
	// version Contract.Version / the .vN.schema.json convention carries. Three adjacent meanings
	// of one word; this is the first kind.
	//
	// Versioning alone does NOT make a published configuration immutable — the plan-time
	// pinned-version guard does (ADR-0119 D6). A same-version content edit would otherwise still
	// update what a pinned environment is running.
	Version int `json:"version,omitempty"`
	// Kind is the payload kind (v1: Intent/Application). Each kind has a
	// schema driving forms/validation.
	Kind string `json:"kind"`
	// Spec is the kind's payload (e.g. {package, channel} for Application) —
	// referenced by Blueprint observe/remediate templates via explicit field
	// lookup ({{.spec.package}}), never an expression language (non-goal).
	Spec map[string]any `json:"spec,omitempty"`
	// OnRemove is the withdrawal lifecycle (retain|revert|remove, default
	// retain). Withdrawn-but-retained state always raises an orphan Finding
	// (§2.4, §4.3).
	OnRemove string `json:"onRemove,omitempty"`
	// Environments is the reconcile-scope MEMBERSHIP FILTER (ADR-0057 D2, ADR-0142 D2) —
	// which environments this Intent applies in. Empty means every environment, exactly as
	// for Assignment, Trigger and Baseline. It is not a value selector and never will be:
	// it chooses WHETHER this document applies, never what it means where it does (ADR-0118
	// D1 guardrail (a)).
	//
	// IT EXISTS BECAUSE A PROVISIONING INTENT HAD NOWHERE TO PUT ONE. An Application Intent
	// is scoped by the Assignment that binds it, but ADR-0058 makes provisioning a sibling
	// reconcile with no Assignment — an Intent/Compute is selected BY NAME — so before this
	// field a provisioning Intent was in force in every environment, unconditionally.
	//
	// The cost of that was not abstract. Which builder a kind resolves to is chosen per
	// environment by a capability-binding (ADR-0151 D2), but the load-time check had to
	// validate the Intent against EVERY provider's builder, since it could not know which
	// environments the Intent would be reconciled in. So `app-tier` CARRIED `region`, `ami`
	// and `instanceType` — AWS coordinates — into a Kubernetes environment where nothing
	// read them, purely to satisfy a builder that would never run for it there; the provider
	// that did build it reported them straight back as ignored, on every build. And
	// admitting a new provisioning provider retroactively invalidated every existing Intent
	// of that kind, because each then had to satisfy the newcomer too.
	//
	// Scoping the Intent is the shape ADR-0118 D1 already prescribes for per-environment
	// values: one flat declaration per environment, rather than one document that means
	// different things in different scopes. Two Intents for a fleet spanning two substrates
	// is that rule applied, not duplication.
	//
	// ADR-0142 D4 in fact PRESUPPOSED this field. Resolving the region coordinate, it wrote
	// that "a flat params.region in each environment-scoped Intent is already the compliant
	// shape ADR-0118 D1 prescribes, and already what the estate does" — but at the time an
	// Intent could not be environment-scoped at all, so D4's stated exit was not yet
	// expressible. The "already" was aspirational; this is the field it was assuming.
	//
	// THE REFERENCE ESTATE IS SCOPED, in a separate change verified by a full-estate
	// in-cluster run rather than by CI alone: `app-tier` and `web-fleet` are
	// `[dev, vsphere-dc]` — every environment in which the estate binds a Compute provider
	// at all — and the coordinates are gone. Refused on assignable kinds (ValidateIntent):
	// an Application Intent is already scoped by the Assignment that binds it, and a second
	// filter there is redundant at best and a disagreeing scope at worst.
	Environments []string `json:"environments,omitempty"`
}
