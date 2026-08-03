package types

import "encoding/json"

// View is a saved, versioned, CaC-declared graph query producing a live
// Entity set (charter §2.1). It unifies what other tools call inventory,
// smart/constructed inventory, Jamf Smart Groups, and SCCM collections.
// Views referenced by Assignments are Git-only — a View edit is a
// blast-radius change (§4.3, §5.4).
type View struct {
	// Name is the stable reference, addressed as view://<name>
	// (e.g. view://retail/kiosk-devices, charter §4.2).
	Name string `json:"name"`
	// Version increments with every declared change; Assignments and Runs
	// record the version they resolved against.
	Version int64 `json:"version"`
	// Selector is the query document. It is structured selector DATA
	// (kind/label/facet predicates) — deliberately not an expression
	// language (non-goal: no new configuration languages, charter §1).
	Selector ViewSelector `json:"selector"`
	// DeclaredBy records which declaration path owns this View: "cac" for
	// the Git-declared desired state (§1.2), "api" for direct declaration.
	// CaC may adopt an api View; the api path may never modify a cac View.
	DeclaredBy string `json:"declaredBy,omitempty"`
}

// ViewSelector is the structured query a View declares. All present clauses
// must match (conjunction). The charter's shorthand view://label:run=X
// (§5.1) desugars to a Labels-only selector.
type ViewSelector struct {
	// Kinds matches Entities whose Kind is any of these (empty = any kind).
	Kinds []string `json:"kinds,omitempty"`
	// Labels matches Entities carrying every listed label key=value.
	Labels map[string]string `json:"labels,omitempty"`
	// Facets matches on Facet values by namespace and JSON path equality,
	// e.g. {namespace: "os.kernel", path: "family", equals: "linux"}.
	Facets []FacetPredicate `json:"facets,omitempty"`
	// Relations matches Entities by an outgoing typed edge — "select the hosts in
	// the DMZ" (ADR-0059 decision 6): the Entity is included if it has a relation of
	// the given type to a target matching targetKind/targetLabels. Selection by
	// TOPOLOGY, not label alone. Additive with the other clauses (AND).
	Relations []RelationPredicate `json:"relations,omitempty"`
}

// RelationPredicate matches an Entity by an outgoing typed edge to a target
// (ADR-0059 decision 6): the Entity (edge FROM) has a Relation of Type to a target
// Entity (edge TO) of TargetKind carrying every TargetLabel. Compiled to an EXISTS
// join over graph.relation — the topology-aware selection clause.
type RelationPredicate struct {
	// Type is the Relation type the Entity must have an outgoing edge of (e.g.
	// "placed-in"). Required.
	Type string `json:"type"`
	// TargetKind optionally constrains the edge's target Entity kind (e.g. "subnet").
	TargetKind string `json:"targetKind,omitempty"`
	// TargetLabels optionally constrains the target Entity's labels (every key=value).
	TargetLabels map[string]string `json:"targetLabels,omitempty"`
}

// FacetPredicate matches one value inside a Facet document.
type FacetPredicate struct {
	Namespace string `json:"namespace"`
	// Path is a dotted path within the Facet value ("" = whole value).
	Path string `json:"path,omitempty"`
	// Equals is the JSON value the addressed field must equal.
	Equals json.RawMessage `json:"equals"`
}

// GroupKey is one partitioning key for a Step's targets (ADR-0161 D2): every DISTINCT value of the
// addressed attribute becomes one group, so values nobody enumerated still produce groups. That is
// `keyed_groups`' generative behaviour, expressed as DATA — a label key or a Facet namespace+path,
// the same structured predicate shape a ViewSelector already uses — rather than as an expression
// language the §1 non-goal forbids.
//
// Exactly one of Label or Facet is set; both or neither is a declaration error, because a key with
// two sources would need a rule to pick between them (§2.4).
type GroupKey struct {
	// Label partitions by the value of this label key. An Entity without the label joins no group
	// from this key — it is not put in an "unknown" bucket, because that would be inventing a value
	// the graph does not carry (§1.2).
	Label string `json:"label,omitempty"`
	// Facet partitions by a value inside a Facet document, addressed exactly as a ViewSelector
	// addresses one. Path "" means the whole value, which is only useful for scalar Facets.
	Facet *FacetKey `json:"facet,omitempty"`
	// Prefix is prepended to the value to form the group name ("region" + "eu-west-1" →
	// "region_eu_west_1"). Optional but strongly advised: two keys whose value spaces overlap
	// would otherwise collide into one group, and the collision would be silent.
	Prefix string `json:"prefix,omitempty"`
}

// FacetKey addresses a value inside a Facet document for grouping. It is FacetPredicate without
// `Equals` — the same addressing, asking for the value rather than testing it.
type FacetKey struct {
	Namespace string `json:"namespace"`
	// Path is a dotted path within the Facet value ("" = the whole value).
	Path string `json:"path,omitempty"`
}
