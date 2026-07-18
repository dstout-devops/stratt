package types

import "time"

// Entity is a graph node: anything with identity — host, VM, device, cert,
// VPC, namespace, account (charter §2.1). An Entity is identity keys + labels
// + a typed document; the document is the union of its Facets, and every
// attribute carries Provenance. There is deliberately no whole-Entity schema
// (charter §1.1): schemas attach at Facets only.
type Entity struct {
	// ID is the platform-assigned stable identifier (UUID).
	ID string `json:"id"`
	// Kind classifies the Entity (e.g. "vm", "host", "cert"). It is a label
	// for querying, not a schema: no ontology hangs off it (§1.1).
	Kind string `json:"kind"`
	// IdentityKeys are the external identities this Entity is known by,
	// namespaced by scheme (e.g. "vcenter.uuid", "dns.fqdn"). Normalizers use
	// them to correlate observations from different Sources onto one node.
	IdentityKeys map[string]string `json:"identityKeys"`
	// Labels are free-form selectors (View queries match on them).
	Labels map[string]string `json:"labels"`
	// ObservedBy is the per-Source presence set backing cross-source liveness
	// (charter §1.2, ADR-0042): the Sources that currently observe this Entity,
	// and when each last saw it. The Entity is live while this is non-empty; it
	// replaces the last-writer-only prov_source_id as the "who vouches for this"
	// answer. Empty for run-only Entities (which stay outside the presence set).
	ObservedBy []SourceObservation `json:"observedBy,omitempty"`
}

// SourceObservation is one Source's presence claim on an Entity: the Source
// (id/kind/name) and the window it has observed the Entity over (§2.1 lineage).
type SourceObservation struct {
	SourceID  string    `json:"sourceId"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
}

// Relation is a typed, directed edge between two Entities:
// runs-on, member-of, issued-by, depends-on (charter §2.1).
type Relation struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	FromID string `json:"fromId"`
	ToID   string `json:"toId"`
}

// Placement Relation types (ADR-0059 decision 2): the topology composition backbone.
// A host is placed-in a subnet; a subnet is in-dmz / in-az. Free-string Relation.Type
// values (§2.1) — no edge-schema change. Written only by the two §1.2 paths (a
// Syncer's observation or a build Run), never a hand-authored graph row.
const (
	RelPlacedIn = "placed-in"
	RelInDmz    = "in-dmz"
	RelInAz     = "in-az"
)
