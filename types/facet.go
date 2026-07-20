package types

import "encoding/json"

// Facet is a named, schema'd fragment of an Entity's document — net.ipv4,
// os.kernel, cert.expiry, apps.installed, mgmt.channels (charter §2.1).
// Facets are where typing hardens progressively: JSON Schema attaches here
// and nowhere else (§1.1), and every Facet schema must be demanded by a
// shipping Contract.
type Facet struct {
	EntityID string `json:"entityId"`
	// Namespace is the dotted Facet name (e.g. "net.ipv4"). Its write owner
	// is declared in the facet-ownership registry; two writers to one
	// namespace is a registration error, never a precedence fight (§2.1).
	Namespace string `json:"namespace"`
	// Value is the Facet document fragment. Typed by the pinned JSON Schema
	// registered for the namespace — validated as data, never as a Go type.
	Value json.RawMessage `json:"value"`
	// Provenance stamps who wrote this value, when, from which Source.
	// Non-optional (§2.1): by construction there is exactly one answer.
	Provenance Provenance `json:"provenance"`
}

// FacetOwner is one row of the facet-ownership registry (charter §2.1): MANY
// sources may be registered to write a namespace (ADR-0060), each retaining its
// own per-source Facet row. `Authoritative` names at most one of them the
// effective "truth" that a scalar read resolves to.
type FacetOwner struct {
	Namespace string `json:"namespace"`
	// OwnerKind is who may write the namespace: a Syncer, a Blueprint output,
	// or a team.
	OwnerKind string `json:"ownerKind"`
	OwnerRef  string `json:"ownerRef"`
	// ViewScope optionally narrows ownership to Entities in a View.
	ViewScope string `json:"viewScope,omitempty"`
	// Authoritative marks this owner the declared effective "truth" for the
	// namespace (ADR-0060): when many sources project it, a scalar read resolves
	// to THIS owner's value. At most one authoritative owner per namespace (§2.4,
	// enforced by a partial unique index). Undeclared → the read fails safe + a
	// contention Finding. sources/ CaC (ADR-0056) later declares this from Git.
	Authoritative bool `json:"authoritative,omitempty"`
}

// LabelOwner is one row of the Entity-label ownership registry (charter §2.1,
// ADR-0038): every label KEY has exactly one declared write owner, so two
// Sources correlating onto one Entity cannot clobber each other's labels
// (§2.4). The label equivalent of FacetOwner, keyed by label key.
type LabelOwner struct {
	Key string `json:"key"`
	// OwnerKind is who may write the key: a Syncer, a Blueprint output, or a team.
	OwnerKind string `json:"ownerKind"`
	OwnerRef  string `json:"ownerRef"`
	// ViewScope optionally narrows ownership to Entities in a View.
	ViewScope string `json:"viewScope,omitempty"`
}
