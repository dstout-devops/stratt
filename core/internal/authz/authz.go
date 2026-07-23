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
	// RelationForwarder authorizes the SIEM egress endpoints on audit:log
	// (ADR-0034): reading batches and advancing the forward cursor. Kept
	// distinct from reader so a read-only audit grant cannot advance a cursor
	// (least-privilege, §1.6) — the forwarder's Principal holds this, humans get
	// reader.
	RelationForwarder = "forwarder"
	// RelationRehome authorizes a fenced cross-Cell Source re-home (ADR-0044
	// slice 7) — a privileged control-plane move of a Source (and its Entities'
	// residency) from one Cell to another. Granted per destination Cell
	// (cell:<dest>), deny-by-default, never implied by org/team admin: moving an
	// estate partition across regions is a deliberate, separately-granted act.
	// The destination Cell re-checks it against the global OpenFGA on the
	// forwarded adopt (§1.6 one authz model), like every other cross-Cell write.
	RelationRehome = "rehome"
	// RelationAdopt authorizes `stratt adopt` (ADR-0086): materializing an observed
	// object into a Stratt-authored Named Kind. Granted per Source (source:<id>),
	// deny-by-default, never implied by reader/admin — flipping an object from
	// foreign-executed to Stratt-executed is a deliberate, separately-granted act
	// (§1.6 one authz model; the API/CLI/agents share this one grant). Distinct from
	// the control-plane-internal `rehome` adopt (ADR-0044).
	RelationAdopt = "adopt"
)

// SourceObject guards a Source for the adopt relation (ADR-0086): a principal must
// hold `adopt` on source:<id> to materialize one of its observed objects.
func SourceObject(source string) string { return "source:" + source }

// ConnectorObject / ActuatorObject guard a Connector/Actuator declaration (ADR-0103). Reads
// follow the v1 open-CaC-read posture (like Triggers); these are the objects a future
// enable/disable toggle or write path would gate on (requireGrant admin/reader).
func ConnectorObject(name string) string { return "connector:" + name }
func ActuatorObject(name string) string  { return "actuator:" + name }

// CellObject guards a Cell for the re-home relation (ADR-0044 slice 7): a
// principal must hold `rehome` on cell:<dest> to move a Source there.
func CellObject(cell string) string { return "cell:" + cell }

// AuditObject is the single object guarding the audit stream (ADR-0034): a
// reader grant authorizes GET /audit and /audit/verify; a forwarder grant
// authorizes the SIEM egress endpoints (batch/report). Audit is privileged
// (who-did-what-when), so it is deny-by-default like Runs — unlike v1's open
// read endpoints.
const AuditObject = "audit:log"

// Authorizer answers relation checks in OpenFGA shape: may `principal` hold
// `relation` on `object` (e.g. "user" on "credential_ref:vcenter-dev")?
// Deny is the default: unknown principals, relations, and objects are false.
type Authorizer interface {
	Check(ctx context.Context, principalID, relation, object string) (bool, error)
	// CheckHealth verifies the authorization backend is reachable — the
	// readiness signal (ADR-0040). Nil = ready.
	CheckHealth(ctx context.Context) error
}

// ApproverAuthorized is the ONE seam that answers "may this principal decide
// this Gate" (§1.6, ADR-0011's deferred note, ADR-0064): a match against the
// declaration-scoped explicit principals list, OR ReBAC membership of one of
// the approver teams via Check. Used by both the human-Gate and policy-opened-
// Gate decision paths, so approver authorization has one implementation. The
// explicit principals list stays declaration data (not an OpenFGA object type),
// checked inside this helper.
func ApproverAuthorized(ctx context.Context, a Authorizer, principal string, principals, teams []string) (bool, error) {
	for _, p := range principals {
		if p == principal {
			return true, nil
		}
	}
	for _, team := range teams {
		member, err := a.Check(ctx, principal, RelationMember, "team:"+team)
		if err != nil {
			return false, err
		}
		if member {
			return true, nil
		}
	}
	return false, nil
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
