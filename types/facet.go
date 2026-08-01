package types

import "encoding/json"

// TeamOwnedFacetNamespaces are the Facet namespaces the platform team owns directly, registered
// by the daemon at boot rather than claimed by any declaration.
//
// Both are stopgaps and are recorded as such where they are registered: `os.kernel` is gathered
// facts written back by every converge and has carried a charter-guardian note since Phase 2, and
// `software.package` has a READER (the patch/advisory check) and no production write-owner —
// ADR-0080 slice 2 (a Syncer-owned collector) is the real answer for it. Until one ships, the team
// owns them exactly as it owns any other unassigned namespace.
//
// EXPORTED because two places must agree on this list and used to hold it separately: the daemon
// that registers the ownership, and the load-time check that refuses a `facetWriteScope` naming a
// namespace nothing owns. A second copy of it would make the check disagree with the runtime about
// what is owned — which is the failure the check exists to prevent, arriving one level up.
func TeamOwnedFacetNamespaces() []string { return []string{"os.kernel", "software.package"} }

// ProjectorOwnedFacetNamespaces are the Facet namespaces an in-core projector owns. Kept separate
// from TeamOwnedFacetNamespaces because the OWNER KIND differs — these register as `syncer`, and
// merging the lists would have the daemon claim one of them as `team` and collide with the
// projector's own registration.
func ProjectorOwnedFacetNamespaces() []string { return []string{"identity.subject"} }

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
	// Qualifier distinguishes several same-namespace Facets on ONE Entity (ADR-0152):
	// `app.config` for apache and `app.config` for tomcat on the same host are two FACTS,
	// not two opinions about one. Empty — the ordinary case — means unqualified.
	//
	// It is DERIVED at compile from the resolved spec and stamped by the core from the
	// claim that owns it; it is never observed, and a writer never proposes its own. An
	// observed key would be undetectable until both Runs had executed, making the winner
	// whoever bound the socket first — execution-order precedence with no field anyone can
	// review (§2.4).
	//
	// DISTINCT FROM THE SOURCE DIMENSION, and the two must never be conflated. ADR-0060's
	// source exists for COMPETING SIGNALS about one fact and collapses to one value at read
	// time via the declared authority; a qualifier is for facts that genuinely COEXIST, and
	// every one of them must survive the read.
	Qualifier string `json:"qualifier,omitempty"`
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
