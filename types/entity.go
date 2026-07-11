package types

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
}

// Relation is a typed, directed edge between two Entities:
// runs-on, member-of, issued-by, depends-on (charter §2.1).
type Relation struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	FromID string `json:"fromId"`
	ToID   string `json:"toId"`
}
