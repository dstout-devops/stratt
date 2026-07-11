package desiredstate

import (
	"context"
	"encoding/json"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// ── ParseDir ─────────────────────────────────────────────────────────────────

func writeDecl(t *testing.T, root, file, content string) {
	t.Helper()
	dir := filepath.Join(root, "views")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseDir(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "prod-linux.yaml", `
name: prod-linux
selector:
  kinds: [vm]
  labels: { env: prod }
  facets:
    - { namespace: os.kernel, path: family, equals: linux }
`)
	writeDecl(t, root, "all-vms.yml", "name: all-vms\nselector:\n  kinds: [vm]\n")
	writeDecl(t, root, "notes.txt", "not a declaration") // ignored

	decls, err := ParseDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(decls) != 2 || decls[0].Name != "all-vms" || decls[1].Name != "prod-linux" {
		t.Fatalf("decls: %+v", decls)
	}
	sel := decls[1].Selector
	if sel.Kinds[0] != "vm" || sel.Labels["env"] != "prod" {
		t.Fatalf("selector: %+v", sel)
	}
	if len(sel.Facets) != 1 || string(sel.Facets[0].Equals) != `"linux"` {
		t.Fatalf("facet equals must canonicalize to JSON: %+v", sel.Facets)
	}
}

func TestParseDirRejects(t *testing.T) {
	// Missing views/ directory is an error, never an empty (prune-all) set.
	if _, err := ParseDir(t.TempDir()); err == nil {
		t.Fatal("missing views/ must be an error")
	}

	root := t.TempDir()
	writeDecl(t, root, "a.yaml", "name: dup\nselector: {kinds: [vm]}\n")
	writeDecl(t, root, "b.yaml", "name: dup\nselector: {kinds: [host]}\n")
	if _, err := ParseDir(root); err == nil || !strings.Contains(err.Error(), "declared in both") {
		t.Fatalf("duplicate names must be rejected, got %v", err)
	}

	root = t.TempDir()
	writeDecl(t, root, "bad.yaml", "name: x\nselektor: {}\n") // typo field
	if _, err := ParseDir(root); err == nil {
		t.Fatal("unknown fields must be rejected (KnownFields)")
	}

	root = t.TempDir()
	writeDecl(t, root, "noname.yaml", "selector: {kinds: [vm]}\n")
	if _, err := ParseDir(root); err == nil {
		t.Fatal("missing name must be rejected")
	}
}

// ── plan/apply against the dev-substrate Postgres ────────────────────────────

// testStore mirrors graph's integration-test helper: throwaway database,
// migrations applied, skip when no substrate is reachable.
func testStore(t *testing.T) *graph.Store {
	t.Helper()
	url := os.Getenv("STRATT_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://stratt:stratt-dev@localhost:5432/stratt"
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}
	name := fmt.Sprintf("stratt_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close()
	})
	u, err := neturl.Parse(url)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	store, err := graph.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect+migrate test database: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func sel(kinds ...string) types.ViewSelector { return types.ViewSelector{Kinds: kinds} }

func actionsByName(p Plan) map[string]Action {
	out := map[string]Action{}
	for _, e := range p.Entries {
		out[e.Name] = e.Action
	}
	return out
}

func TestPlanApplyLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// An api-declared View that the desired state will adopt.
	if _, err := s.DeclareView(ctx, "adoptme", sel("vm")); err != nil {
		t.Fatal(err)
	}

	decls := []Declaration{
		{Name: "adoptme", Selector: sel("vm")},
		{Name: "fresh", Selector: sel("vm")},
	}
	plan, err := ComputePlan(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	got := actionsByName(plan)
	if got["adoptme"] != ActionAdopt || got["fresh"] != ActionCreate {
		t.Fatalf("plan: %+v", got)
	}

	applied, err := Apply(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range applied.Entries {
		if e.Error != "" {
			t.Fatalf("apply error on %s: %s", e.Name, e.Error)
		}
	}

	// Adoption transfers ownership without minting a version.
	v, err := s.GetView(ctx, "adoptme")
	if err != nil {
		t.Fatal(err)
	}
	if v.DeclaredBy != graph.DeclaredByCaC || v.Version != 1 {
		t.Fatalf("adoption should transfer ownership at the same version, got %+v", v)
	}

	// Re-apply: everything noop.
	plan2, err := Apply(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	if plan2.Changes() != 0 {
		t.Fatalf("re-apply should be all noop: %+v", plan2.Entries)
	}

	// Selector change → update, version bump.
	decls[1].Selector = sel("vm", "host")
	plan3, err := Apply(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	if actionsByName(plan3)["fresh"] != ActionUpdate {
		t.Fatalf("changed selector should plan update: %+v", plan3.Entries)
	}
	v, err = s.GetView(ctx, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != 2 {
		t.Fatalf("update should bump version, got %d", v.Version)
	}

	// The api path may not touch a cac View (§2.1: Git only).
	if _, err := s.DeclareView(ctx, "fresh", sel("vm")); err == nil || !strings.Contains(err.Error(), "cac") {
		t.Fatalf("api declare on cac view must fail with the cac guard, got %v", err)
	}

	// Prune: removing a declaration deletes the cac View, and only it.
	if _, err := s.DeclareView(ctx, "api-owned", sel("vm")); err != nil {
		t.Fatal(err)
	}
	plan4, err := Apply(ctx, s, decls[:1]) // only adoptme remains declared
	if err != nil {
		t.Fatal(err)
	}
	if actionsByName(plan4)["fresh"] != ActionDelete {
		t.Fatalf("undeclared cac view should be pruned: %+v", plan4.Entries)
	}
	if _, ok := actionsByName(plan4)["api-owned"]; ok {
		t.Fatal("api-declared views must never be prune candidates")
	}
	if _, err := s.GetView(ctx, "fresh"); err == nil {
		t.Fatal("pruned view should be gone")
	}
	if _, err := s.GetView(ctx, "api-owned"); err != nil {
		t.Fatalf("api view must survive: %v", err)
	}

	// Deleted name can be re-declared: history went with the row.
	decls2 := append(decls[:1], Declaration{Name: "fresh", Selector: sel("vm")})
	plan5, err := Apply(ctx, s, decls2)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range plan5.Entries {
		if e.Error != "" {
			t.Fatalf("re-declare after prune failed on %s: %s", e.Name, e.Error)
		}
	}
}

func TestPruneStats(t *testing.T) {
	p := Plan{Entries: []PlanEntry{
		{Name: "a", Action: ActionNoop},
		{Name: "b", Action: ActionUpdate},
		{Name: "c", Action: ActionDelete},
		{Name: "d", Action: ActionDelete},
		{Name: "e", Action: ActionCreate}, // not a current cac view
		{Name: "f", Action: ActionAdopt},  // currently api, not cac
	}}
	deletes, cacTotal := p.PruneStats()
	if deletes != 2 || cacTotal != 4 {
		t.Fatalf("prune stats: deletes=%d cacTotal=%d", deletes, cacTotal)
	}
}

func TestSelectorsEqualNormalization(t *testing.T) {
	a := types.ViewSelector{Kinds: []string{"vm"}, Facets: []types.FacetPredicate{
		{Namespace: "os.kernel", Path: "family", Equals: json.RawMessage(`"linux"`)},
	}}
	b := types.ViewSelector{Kinds: []string{"vm"}, Facets: []types.FacetPredicate{
		{Namespace: "os.kernel", Path: "family", Equals: json.RawMessage(` "linux" `)},
	}}
	if !selectorsEqual(a, b) {
		t.Fatal("whitespace-only raw JSON difference must not read as drift")
	}
	b.Facets[0].Equals = json.RawMessage(`"windows"`)
	if selectorsEqual(a, b) {
		t.Fatal("different equals must read as drift")
	}
}
