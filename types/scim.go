package types

import (
	"encoding/json"
	"time"
)

// SCIM (charter §8, ADR-0035): Stratt is a SCIM 2.0 Service Provider — the
// customer's IdP pushes Users and Groups, which Stratt projects into a born-here
// identity registry (the `scim` schema). These types are the DOMAIN view of that
// registry and its CaC config; the SCIM wire structs (User/Group/ListResponse/
// PatchOp/Error) live in core/internal/scim — they are protocol, not domain.
//
// "SCIM", "identity", and "group" are protocol/delivery nouns here, NOT §2 Named
// Kinds. The registry BACKS the one Principal model (§1.6): a provisioned
// identity's PrincipalID is the value the OIDC `sub` carries at request time.

// SCIMIdP is a CaC-declared authorized IdP: which IdP may push (by name), the
// sha256 of its bearer token (§2.5 — material never stored), and its group→team
// mappings. Written only by the desired-state engine (sole writer of scim.idp),
// mirroring Emitter/Sink CaC config.
type SCIMIdP struct {
	Name string `json:"name"`
	// TokenHash is hex(sha256(bearerToken)) — the SP authenticates each push by
	// constant-time comparing against it, exactly like an Emitter token.
	TokenHash string `json:"tokenHash"`
	// GroupMappings bind an IdP group to a Stratt team. A mapped team's
	// MEMBERSHIP becomes IdP-owned (§2.1 one-owner); policy/role-grants stay CaC.
	// Unmapped groups are projected-but-ungranted.
	GroupMappings []GroupMapping `json:"groupMappings,omitempty"`
}

// GroupMapping binds one IdP group (by displayName) to a Stratt team.
type GroupMapping struct {
	Group string `json:"group"`
	Team  string `json:"team"`
}

// SCIMIdentity is one provisioned User in the registry.
type SCIMIdentity struct {
	IDP         string          `json:"idp"`
	SCIMID      string          `json:"scimId"`
	UserName    string          `json:"userName"`
	ExternalID  string          `json:"externalId,omitempty"`
	PrincipalID string          `json:"principalId"`
	Active      bool            `json:"active"`
	Emails      json.RawMessage `json:"emails,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// SCIMGroup is one provisioned Group in the registry.
type SCIMGroup struct {
	IDP         string    `json:"idp"`
	SCIMID      string    `json:"scimId"`
	DisplayName string    `json:"displayName"`
	ExternalID  string    `json:"externalId,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// SCIMMembership is one resolved, projectable membership edge: an active
// provisioned identity is a member of a mapped team. The reconcile loop turns
// each into an authz tuple (principal:<PrincipalID> member team:<Team>) and
// unions it with the CaC tuple set before the authoritative OpenFGA sync.
type SCIMMembership struct {
	PrincipalID string `json:"principalId"`
	Team        string `json:"team"`
}
