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
