package authz

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func loadFixture(t *testing.T, doc string) *TupleAuthorizer {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "authz"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "authz", "tuples.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &TupleAuthorizer{}
	if err := a.LoadTuples(root); err != nil {
		t.Fatal(err)
	}
	return a
}

const fixture = `
tuples:
  # org: nebulae; alice is org admin; team platform belongs to nebulae
  - { user: "principal:alice", relation: admin, object: "org:nebulae" }
  - { user: "org:nebulae", relation: org, object: "team:platform" }
  # bob is a direct team member; carol is team admin
  - { user: "principal:bob", relation: member, object: "team:platform" }
  - { user: "principal:carol", relation: admin, object: "team:platform" }
  # the credential belongs to platform; team members may USE it
  - { user: "team:platform", relation: owner_team, object: "credential_ref:vc" }
  - { user: "team:platform#member", relation: user, object: "credential_ref:vc" }
  # dave has use directly, nothing else; erin has read only
  - { user: "principal:dave", relation: user, object: "credential_ref:vc" }
  - { user: "principal:erin", relation: reader, object: "credential_ref:vc" }
  # View-scoped execution (§2.5, ADR-0028): prod is owned by platform; members
  # may RUN it. dave may run solo directly (nothing on prod — runner is per-View).
  # frank may read prod but not run it (reader ≠ runner).
  - { user: "team:platform", relation: owner_team, object: "view:prod" }
  - { user: "team:platform#member", relation: runner, object: "view:prod" }
  - { user: "principal:dave", relation: runner, object: "view:solo" }
  - { user: "principal:frank", relation: reader, object: "view:prod" }
  # A View in a DIFFERENT org — the no-blanket-org-bypass proof: nebulae's admin
  # (alice) must have NO runner here; contractors' member zoe does.
  - { user: "org:other", relation: org, object: "team:contractors" }
  - { user: "team:contractors", relation: owner_team, object: "view:ext" }
  - { user: "team:contractors#member", relation: runner, object: "view:ext" }
  - { user: "principal:zoe", relation: member, object: "team:contractors" }
`

func TestTupleSemantics(t *testing.T) {
	a := loadFixture(t, fixture)
	ctx := context.Background()
	check := func(p, rel, obj string) bool {
		ok, err := a.Check(ctx, p, rel, obj)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	// Direct membership and admin⊃member.
	if !check("bob", "member", "team:platform") {
		t.Fatal("direct member")
	}
	if !check("carol", "member", "team:platform") {
		t.Fatal("team admin implies member")
	}

	// Org admin ⊃ team admin ⊃ member ⊃ (via userset) credential use.
	if !check("alice", "admin", "team:platform") {
		t.Fatal("org admin implies team admin")
	}
	if !check("alice", "member", "team:platform") {
		t.Fatal("org admin implies team member")
	}
	if !check("alice", "user", "credential_ref:vc") {
		t.Fatal("org admin is a team member, so userset use applies")
	}

	// Owner-team admin ⊃ object admin ⊃ reader.
	if !check("carol", "admin", "credential_ref:vc") {
		t.Fatal("owner team admin implies credential admin")
	}
	if !check("carol", "reader", "credential_ref:vc") {
		t.Fatal("admin implies reader")
	}

	// Userset use for members.
	if !check("bob", "user", "credential_ref:vc") {
		t.Fatal("team member gets userset use")
	}

	// USE IMPLIES NOTHING (use-without-read, §2.5): dave may use but not
	// read; bob (member with userset use) may not read either.
	if !check("dave", "user", "credential_ref:vc") {
		t.Fatal("direct use grant")
	}
	if check("dave", "reader", "credential_ref:vc") {
		t.Fatal("use must NOT imply read")
	}
	if check("dave", "admin", "credential_ref:vc") {
		t.Fatal("use must NOT imply admin")
	}
	if check("bob", "reader", "credential_ref:vc") {
		t.Fatal("member use must NOT imply read")
	}

	// Reader implies nothing upward.
	if check("erin", "user", "credential_ref:vc") {
		t.Fatal("read must NOT imply use")
	}
	if check("erin", "admin", "credential_ref:vc") {
		t.Fatal("read must NOT imply admin")
	}

	// Unknowns are denied.
	if check("mallory", "user", "credential_ref:vc") || check("bob", "user", "credential_ref:other") {
		t.Fatal("default deny")
	}
}

// TestViewScopedExecution covers the ADR-0028 view/runner semantics.
func TestViewScopedExecution(t *testing.T) {
	a := loadFixture(t, fixture)
	ctx := context.Background()
	check := func(p, rel, obj string) bool {
		ok, err := a.Check(ctx, p, rel, obj)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	// Runner via team#member userset, via owner-team admin, and directly.
	if !check("bob", "runner", "view:prod") {
		t.Fatal("team member gets userset runner")
	}
	if !check("carol", "runner", "view:prod") {
		t.Fatal("owner-team admin implies view admin implies runner")
	}
	if !check("dave", "runner", "view:solo") {
		t.Fatal("direct runner grant")
	}

	// runner is PER-VIEW, never global: dave runs solo, not prod.
	if check("dave", "runner", "view:prod") {
		t.Fatal("runner must be view-scoped, not global")
	}
	if check("bob", "runner", "view:solo") {
		t.Fatal("prod member has no runner on solo")
	}

	// reader ≠ runner (siblings; neither implies the other).
	if !check("frank", "reader", "view:prod") {
		t.Fatal("direct reader grant")
	}
	if check("frank", "runner", "view:prod") {
		t.Fatal("reader must NOT imply runner")
	}
	if check("bob", "reader", "view:prod") {
		// bob has runner (member userset) but no reader grant/userset.
		t.Fatal("runner must NOT imply reader")
	}

	// NO blanket org-admin bypass (the user's decision): alice administers
	// nebulae (and thus platform → view:prod, legitimately by ownership), but
	// has NO path to view:ext, owned by contractors in a different org.
	if !check("alice", "runner", "view:prod") {
		t.Fatal("org admin reaches an owned View via the ownership chain")
	}
	if check("alice", "runner", "view:ext") {
		t.Fatal("org admin must NOT get runner on a View it does not own (no bypass)")
	}
	if !check("zoe", "runner", "view:ext") {
		t.Fatal("contractors member gets runner on its own View")
	}

	// Default deny.
	if check("mallory", "runner", "view:prod") || check("mallory", "runner", "view:ext") {
		t.Fatal("ungranted principal denied")
	}
}

func TestLoadTuplesSafety(t *testing.T) {
	// Missing file → empty set (deny everything), not an error.
	a := &TupleAuthorizer{}
	if err := a.LoadTuples(t.TempDir()); err != nil {
		t.Fatalf("missing tuples file must be an empty set: %v", err)
	}
	if ok, _ := a.Check(context.Background(), "alice", "admin", "org:nebulae"); ok {
		t.Fatal("empty set must deny")
	}

	// Unparseable file → error (never silently drop grants).
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "authz"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "authz", "tuples.yaml"), []byte("tuples: [{userz: x}]"), 0o644)
	if err := a.LoadTuples(root); err == nil {
		t.Fatal("bad tuples must error")
	}

	// A reload failure must not clear previously loaded tuples implicitly —
	// the caller decides; verify state unchanged after failed load.
	b := loadFixture(t, fixture)
	_ = os.WriteFile(filepath.Join(root, "authz", "tuples.yaml"), []byte("nonsense: ["), 0o644)
	if err := b.LoadTuples(root); err == nil {
		t.Fatal("expected error")
	}
	if ok, _ := b.Check(context.Background(), "bob", "member", "team:platform"); !ok {
		t.Fatal("failed reload must keep the previous grant set")
	}
}
