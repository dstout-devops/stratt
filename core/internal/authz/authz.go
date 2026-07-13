// Package authz is the platform's one authorization seam (charter §2.5,
// ADR-0009): ReBAC in OpenFGA's terms — org → team → Principal, object-level
// grants, use-without-read. This phase enforces via an in-process tuple
// evaluator over CaC tuple manifests; the OpenFGA server replaces the
// evaluator behind the same interface when OIDC lands.
package authz

import (
	"context"
)

// Principal identity kinds — one model for all three (§2.5).
const (
	KindHuman   = "human"
	KindService = "service"
	KindAgent   = "agent"
)

// Relations used by the v1 model (ADR-0009).
const (
	RelationAdmin  = "admin"
	RelationMember = "member"
	RelationReader = "reader"
	// RelationUser is use-without-read: it implies NOTHING else.
	RelationUser = "user"
	// RelationRunner is View-scoped execution (§2.5, ADR-0028): may launch a
	// Run/Workflow against a view:<name> — "only against Entities in this
	// View." Granted per-View, never blanket by org/team admin.
	RelationRunner = "runner"
)

// Authorizer answers relation checks in OpenFGA shape: may `principal` hold
// `relation` on `object` (e.g. "user" on "credential_ref:vcenter-dev")?
// Deny is the default: unknown principals, relations, and objects are false.
type Authorizer interface {
	Check(ctx context.Context, principalID, relation, object string) (bool, error)
}

type ctxKey struct{}

// WithPrincipal attaches the resolved Principal to the request context.
func WithPrincipal(ctx context.Context, id, kind string) context.Context {
	return context.WithValue(ctx, ctxKey{}, [2]string{id, kind})
}

// PrincipalFrom returns the request's Principal id and kind; ok=false means
// the request is anonymous (denied everything but health).
func PrincipalFrom(ctx context.Context) (id, kind string, ok bool) {
	v, found := ctx.Value(ctxKey{}).([2]string)
	if !found || v[0] == "" {
		return "", "", false
	}
	return v[0], v[1], true
}
