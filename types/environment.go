package types

// Environment is a declared RECONCILE SCOPE (ADR-0142): which slice of one estate repo a
// daemon applies, selected by STRATT_ENVIRONMENT and matched against each declaration's
// `environments` membership filter (ADR-0057).
//
// It exists so that filter has a REFERENT. Before this, an environment was referenced
// everywhere and declared nowhere, so `environments: [us-east-01]` — a typo — filtered a
// declaration out of every environment permanently, with no error at load, no warning at
// reconcile and no Finding. The estate read as correct in Git and did nothing (§1.8).
//
// NOT a Named Kind: §2 is frozen, and `environments` is an existing field on Assignments,
// Triggers, Baselines, Connectors and CapabilityBindings. This gives that field something
// to point at, exactly as CapabilityBinding gave `requires: [<class>]` a resolution
// without adding vocabulary.
//
// DELIBERATELY THIN, and the thinness is the decision (ADR-0142 D4). It carries no region
// coordinate and no DNS zone, because a consumer INHERITING those would make one Intent
// document mean different things per environment — the env-conditional-values shape
// ADR-0118 D1 forbids ("environments is a membership filter, not a value selector"). That
// question is deferred to its own charter-guardian review; until then the compliant shape
// is one declaration per environment, each carrying flat values.
//
// It is none of the following, and the confusion is common enough to name here (ADR-0142 D3):
//   - a Cell (ADR-0044) — a region-local control-plane SHARD with its own
//     Postgres/NATS/Temporal/OpenFGA. Cell.Region is the region the CONTROL PLANE runs in;
//     one control plane routinely manages many substrate regions.
//   - a Site (ADR-0032) — an execution LOCUS. A Cell contains Sites.
//   - a provider coordinate — `params.region`, the substrate's own opaque name for a place.
//   - a region Entity (ADR-0059/0115) — an OBSERVED fact a Syncer projects, not a declared scope.
type Environment struct {
	// Name is the token an `environments:` entry and STRATT_ENVIRONMENT match on.
	Name string `json:"name"`
	// Description is operator-facing metadata: what this environment is for. Metadata only —
	// nothing resolves against it.
	Description string `json:"description,omitempty"`
}
