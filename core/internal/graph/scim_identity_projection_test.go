package graph

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestProjectSCIMEntities proves ADR-0079 slice 3: the SCIM registry projects into
// the graph as user/group Entities carrying identity.subject, with member-of
// Relations — status projected from the SoR (INV-2), idempotent, single §2.1 owner.
func TestProjectSCIMEntities(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	// Seed a SCIM IdP: two users (bob deactivated at the IdP) and a group both belong to.
	if err := store.UpsertIDP(ctx, types.SCIMIdP{Name: "okta", TokenHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertIdentity(ctx, types.SCIMIdentity{IDP: "okta", SCIMID: "u1", UserName: "alice", PrincipalID: "alice@x", Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertIdentity(ctx, types.SCIMIdentity{IDP: "okta", SCIMID: "u2", UserName: "bob", PrincipalID: "bob@x", Active: false}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertGroup(ctx, types.SCIMGroup{IDP: "okta", SCIMID: "g1", DisplayName: "eng"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceGroupMembers(ctx, "okta", "g1", []string{"u1", "u2"}); err != nil {
		t.Fatal(err)
	}

	if err := store.EnsureIdentitySubjectOwner(ctx); err != nil {
		t.Fatalf("register owner: %v", err)
	}
	if err := store.ProjectSCIMEntities(ctx); err != nil {
		t.Fatalf("project: %v", err)
	}

	// user/group Entities exist.
	if got := count(t, store, `SELECT count(*) FROM graph.entity WHERE kind='user' AND deleted_at IS NULL`); got != 2 {
		t.Fatalf("want 2 user Entities, got %d", got)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.entity WHERE kind='group' AND deleted_at IS NULL`); got != 1 {
		t.Fatalf("want 1 group Entity, got %d", got)
	}

	// identity.subject reflects the SoR status: alice active, bob disabled (INV-2 —
	// projected, never authored).
	if s := subjectStatus(t, store, "alice"); s != "active" {
		t.Fatalf("alice status = %q, want active", s)
	}
	if s := subjectStatus(t, store, "bob"); s != "disabled" {
		t.Fatalf("bob status = %q, want disabled", s)
	}

	// member-of Relations: both users → the group.
	if got := count(t, store, `SELECT count(*) FROM graph.relation WHERE type='member-of'`); got != 2 {
		t.Fatalf("want 2 member-of Relations, got %d", got)
	}

	// Idempotent: a second projection converges (no duplicate Entities/Relations).
	if err := store.ProjectSCIMEntities(ctx); err != nil {
		t.Fatalf("re-project: %v", err)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.entity WHERE kind='user' AND deleted_at IS NULL`); got != 2 {
		t.Fatalf("after re-project want 2 users, got %d", got)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.relation WHERE type='member-of'`); got != 2 {
		t.Fatalf("after re-project want 2 member-of, got %d", got)
	}
}

func count(t *testing.T, s *Store, q string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("count query: %v", err)
	}
	return n
}

func subjectStatus(t *testing.T, s *Store, userName string) string {
	t.Helper()
	var raw []byte
	err := s.pool.QueryRow(context.Background(), `
		SELECT f.value FROM graph.facet f
		JOIN graph.entity e ON e.id = f.entity_id
		WHERE e.kind='user' AND e.labels->>'identity.name'=$1 AND f.namespace='identity.subject'`,
		userName).Scan(&raw)
	if err != nil {
		t.Fatalf("read identity.subject for %q: %v", userName, err)
	}
	var subj struct{ Scheme, Name, Status string }
	if err := json.Unmarshal(raw, &subj); err != nil {
		t.Fatalf("unmarshal identity.subject: %v", err)
	}
	if subj.Scheme != "user" || subj.Name != userName {
		t.Fatalf("identity.subject shape: %+v", subj)
	}
	return subj.Status
}
