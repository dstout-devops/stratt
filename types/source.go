package types

// Source is an external system of record, registered with CredentialRefs and
// trust settings (charter §2.2). Sources stay authoritative — the graph only
// ever holds a rebuildable projection of them (§1.2).
type Source struct {
	ID string `json:"id"`
	// Kind names the class of system (e.g. "vcenter").
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Endpoint is the connection locator (URL/host). Secret material is
	// never stored here — only a CredentialRef pointer (§2.5).
	Endpoint      string `json:"endpoint"`
	CredentialRef string `json:"credentialRef,omitempty"`
	// Cell is the control-plane Cell this Source homes to (ADR-0044): the Cell
	// of the daemon that registered it. Its Entities inherit this home. Empty ⇒
	// the built-in LocalCell.
	Cell string `json:"cell,omitempty"`
	// RehomingTo is the destination Cell when this Source is SEALED mid re-home
	// (ADR-0044 slice 7); empty when settled. While set, the home Cell's
	// Normalizer writes for this Source are rejected by enforce_write_path — the
	// single-writer fence during the cross-Cell hand-off (§2.1).
	RehomingTo string `json:"rehomingTo,omitempty"`
	// HomeEpoch is the per-Source fencing token, bumped on every seal so the
	// destination Cell can reject a stale/replayed adopt (idempotency). The
	// authoritative ordering is the RehomeSourceWorkflow's history, not a
	// cross-Cell epoch compare (separate Postgres, no cross-DB CAS).
	HomeEpoch int64 `json:"homeEpoch,omitempty"`
}

// RehomeState is a phase in a fenced Source re-home, carried on the audit detail
// and the cross-Cell adopt payload (ADR-0044 slice 7).
const (
	RehomeSealed   = "sealed"   // source Cell fenced the Source (writes rejected)
	RehomeAdopted  = "adopted"  // destination Cell claimed the Source
	RehomeComplete = "complete" // source Cell retired the old Entities (tombstoned)
	RehomeAborted  = "aborted"  // pre-adopt failure: source Cell un-sealed
)

// FindingRehomeStuck is the framework label for the §1.8 live-surface Finding a
// stuck seal raises (a re-home sealed but not completed — partition, unreachable
// peer, or a Connector not yet deployed on the destination). Auto-resolves on
// complete or abort. Distinct from 'placement' (a homing-vs-observed mismatch).
const FindingRehomeStuck = "rehome"

// ResolvedEntityRehomed marks a Finding resolved because the Entity it concerned
// was tombstoned as part of a Source re-home (ADR-0044 slice 7) — distinct from
// 'entity-tombstoned' (ADR-0043) so descent shows the Entity moved Cells, it did
// not vanish.
const ResolvedEntityRehomed = "entity-rehomed"
