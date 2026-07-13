package authz

import (
	"context"
	"net"
	"net/url"
	"os"
	"testing"
	"time"
)

// openFGAURL returns the dev server's URL, skipping when unreachable — the
// same pattern as the DB-backed tests: unit runs stay substrate-free, `task
// test` with the dev compose up exercises the real server.
func openFGAURL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("STRATT_OPENFGA_URL")
	if raw == "" {
		raw = "http://localhost:8081"
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("bad STRATT_OPENFGA_URL: %v", err)
	}
	conn, err := net.DialTimeout("tcp", u.Host, 500*time.Millisecond)
	if err != nil {
		t.Skipf("openfga unreachable at %s — start it with `task dev:up`", raw)
	}
	conn.Close()
	return raw
}

// TestOpenFGAAgreement is the swap-fidelity proof (ADR-0009): the in-process
// TupleAuthorizer defines the v1 model's semantics; the server, loaded with
// the same CaC fixture via SyncTuples, must answer every check identically.
func TestOpenFGAAgreement(t *testing.T) {
	ctx := context.Background()
	fga, err := NewOpenFGAAuthorizer(ctx, openFGAURL(t))
	if err != nil {
		t.Fatal(err)
	}
	ref := loadFixture(t, fixture)
	if err := fga.SyncTuples(ctx, ref.Snapshot()); err != nil {
		t.Fatal(err)
	}

	principals := []string{"alice", "bob", "carol", "dave", "erin", "frank", "zoe", "mallory"}
	// Relations checkable per object type — OpenFGA validation-errors on
	// relations a type does not define, where the evaluator simply denies.
	objectRelations := map[string][]string{
		"org:nebulae":          {RelationAdmin, RelationMember},
		"team:platform":        {RelationAdmin, RelationMember},
		"credential_ref:vc":    {RelationAdmin, RelationReader, RelationUser},
		"credential_ref:other": {RelationAdmin, RelationReader, RelationUser},
		"view:prod":            {RelationAdmin, RelationReader, RelationRunner},
		"view:solo":            {RelationAdmin, RelationReader, RelationRunner},
		"view:ext":             {RelationAdmin, RelationReader, RelationRunner},
	}
	checked := 0
	for _, p := range principals {
		for obj, relations := range objectRelations {
			for _, rel := range relations {
				want, err := ref.Check(ctx, p, rel, obj)
				if err != nil {
					t.Fatal(err)
				}
				got, err := fga.Check(ctx, p, rel, obj)
				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("disagreement: %s %s %s — evaluator=%v openfga=%v", p, rel, obj, want, got)
				}
				checked++
			}
		}
	}
	t.Logf("agreement across %d checks", checked)

	// Sync is authoritative, not additive: shrinking the manifest revokes.
	if err := fga.SyncTuples(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := fga.Check(ctx, "bob", RelationUser, "credential_ref:vc"); err != nil || ok {
		t.Fatalf("emptied manifest must revoke (ok=%v err=%v)", ok, err)
	}

	// Idempotent re-ensure: constructing again must not error or duplicate.
	if _, err := NewOpenFGAAuthorizer(ctx, openFGAURL(t)); err != nil {
		t.Fatal(err)
	}
}
