package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	neturl "net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

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
	u, _ := neturl.Parse(url)
	u.Path = "/" + name
	store, err := graph.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect+migrate test database: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

// seedEntity creates a vm Entity with the given os.kernel facet and returns id.
func seedEntity(t *testing.T, s *graph.Store, uuid, arch string) string {
	t.Helper()
	ctx := context.Background()
	// os.kernel is Syncer-owned (registration precedes writes, §2.1) — a
	// Blueprint observing it reads only, never seizes ownership.
	_ = s.RegisterFacetOwner(ctx, types.FacetOwner{Namespace: "os.kernel", OwnerKind: "syncer", OwnerRef: "test/syncer"})
	p := s.NormalizerProjector()
	prov := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "test/syncer", At: time.Now().UTC()}
	ids, err := p.UpsertEntities(ctx, prov, []graph.EntityUpsert{
		{Kind: "vm", IdentityKeys: map[string]string{"vcenter.uuid": uuid}, Labels: map[string]string{"env": "test"}},
	})
	if err != nil {
		t.Fatalf("seed entity: %v", err)
	}
	if arch != "" {
		if err := p.UpsertFacet(ctx, prov, ids[0], "os.kernel",
			json.RawMessage(fmt.Sprintf(`{"family":"linux","arch":%q}`, arch))); err != nil {
			t.Fatalf("seed facet: %v", err)
		}
	} else {
		if err := p.UpsertFacet(ctx, prov, ids[0], "os.kernel", json.RawMessage(`{"family":"linux"}`)); err != nil {
			t.Fatalf("seed facet (no arch): %v", err)
		}
	}
	return ids[0]
}

func seedView(t *testing.T, s *graph.Store, name string) {
	t.Helper()
	if _, err := s.DeclareViewAs(context.Background(), name,
		types.ViewSelector{Kinds: []string{"vm"}}, graph.DeclaredByCaC); err != nil {
		t.Fatalf("seed view: %v", err)
	}
}

func appBlueprint(name string, version int, claim string) types.Blueprint {
	return types.Blueprint{
		Name: name, Version: version, For: types.IntentApplication,
		Severity: types.SeverityWarning, DampingObservations: 1,
		Routes: []types.BlueprintRoute{{
			Match: []types.FacetPredicate{{Namespace: "os.kernel", Path: "family", Equals: json.RawMessage(`"linux"`)}},
			Observe: types.FacetExpectation{
				Namespace: "os.kernel", Path: "arch", Equals: json.RawMessage(`"x86_64"`),
			},
			Claim: claim,
		}},
	}
}

func compileApply(t *testing.T, s *graph.Store, maxDelta float64) Plan {
	t.Helper()
	plan, err := Compile(context.Background(), s, maxDelta)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if errs := plan.Apply(context.Background(), s); len(errs) > 0 {
		// Apply errors are surfaced on the plan; tests assert on plan.Errors.
		plan.Errors = errs
	}
	return plan
}

func TestCompileHappyPath(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e1 := seedEntity(t, s, "u1", "x86_64") // meets expectation
	seedEntity(t, s, "u2", "")             // lacks arch → will drift
	seedView(t, s, "dev-vms")

	must(t, s.UpsertIntent(ctx, types.Intent{Name: "chrome", Kind: types.IntentApplication, OnRemove: types.OnRemoveRetain}))
	must(t, s.UpsertBlueprint(ctx, appBlueprint("application", 3, types.ClaimAdditive)))
	must(t, s.UpsertAssignment(ctx, types.Assignment{Name: "kiosks", Intent: "chrome", View: "dev-vms", Blueprint: "application", BlueprintVersion: 3}))

	plan := compileApply(t, s, 0)
	if len(plan.Errors) != 0 {
		t.Fatalf("unexpected compile errors: %v", plan.Errors)
	}
	name := CompiledName("kiosks", "application", 3, 0)
	b, err := s.GetBaseline(ctx, name)
	if err != nil {
		t.Fatalf("compiled baseline missing: %v", err)
	}
	if b.Mode != types.FacetObservation || b.CompiledFrom == nil || b.CompiledFrom.Assignment != "kiosks" {
		t.Fatalf("compiled baseline shape: %+v", b)
	}
	if b.Selector == nil || len(b.Selector.Facets) != 1 {
		t.Fatalf("compiled selector must carry the route match: %+v", b.Selector)
	}
	// os.kernel is Syncer-observed: the Blueprint reads it, never seizes
	// write-ownership (the syncer stays the owner).
	owner, ok, _ := s.GetFacetOwner(ctx, "os.kernel")
	if !ok || owner.OwnerKind != "syncer" {
		t.Fatalf("os.kernel must remain syncer-owned, got %+v ok=%v", owner, ok)
	}
	// Membership snapshot persisted with both entities.
	m, ok, _ := s.GetAssignmentMembership(ctx, "kiosks")
	if !ok || m.MemberCount != 2 {
		t.Fatalf("membership: %+v ok=%v", m, ok)
	}
	_ = e1
}

func TestCompileClaimConflict(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	seedEntity(t, s, "u1", "x86_64")
	seedView(t, s, "dev-vms")

	must(t, s.UpsertIntent(ctx, types.Intent{Name: "chrome", Kind: types.IntentApplication}))
	must(t, s.UpsertIntent(ctx, types.Intent{Name: "firefox", Kind: types.IntentApplication}))
	// Two blueprints, same observe namespace, both exclusive.
	must(t, s.UpsertBlueprint(ctx, appBlueprint("app-a", 1, types.ClaimExclusive)))
	bpB := appBlueprint("app-a", 1, types.ClaimExclusive) // same name/version reused? no — distinct
	bpB.Name = "app-b"
	must(t, s.UpsertBlueprint(ctx, bpB))
	must(t, s.UpsertAssignment(ctx, types.Assignment{Name: "asgA", Intent: "chrome", View: "dev-vms", Blueprint: "app-a", BlueprintVersion: 1}))
	must(t, s.UpsertAssignment(ctx, types.Assignment{Name: "asgB", Intent: "firefox", View: "dev-vms", Blueprint: "app-b", BlueprintVersion: 1}))

	plan := compileApply(t, s, 0)
	if len(plan.Errors) == 0 {
		t.Fatal("expected an exclusive claim-conflict error")
	}
	// Neither conflicting assignment's baseline is applied (no partial apply).
	if _, err := s.GetBaseline(ctx, CompiledName("asgA", "app-a", 1, 0)); err == nil {
		t.Fatal("conflicted assignment asgA must not produce a baseline")
	}
	if _, err := s.GetBaseline(ctx, CompiledName("asgB", "app-b", 1, 0)); err == nil {
		t.Fatal("conflicted assignment asgB must not produce a baseline")
	}
	// The second blueprint's ownership claim on the shared namespace is denied.
	// (First registrant wins the namespace; the conflict is reported.)
}

// ownedBlueprint manages (remediates) a fresh namespace, so the compiler
// claims Blueprint write-ownership of it — the input to the conflict test.
func ownedBlueprint(name string, version int, observeNS string) types.Blueprint {
	return types.Blueprint{
		Name: name, Version: version, For: types.IntentApplication,
		Severity: types.SeverityWarning, DampingObservations: 1,
		Routes: []types.BlueprintRoute{{
			Match:   []types.FacetPredicate{{Namespace: "os.kernel", Path: "family", Equals: json.RawMessage(`"linux"`)}},
			Observe: types.FacetExpectation{Namespace: observeNS, Equals: json.RawMessage(`"present"`)},
			// additive: isolates the blueprint-vs-blueprint OWNERSHIP conflict
			// from the exclusive claim-type conflict. A remediation Workflow
			// marks the namespace as MANAGED (write-claimed).
			Claim:               types.ClaimAdditive,
			RemediationWorkflow: "install",
		}},
	}
}

func TestCompileOwnershipConflict(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	seedEntity(t, s, "u1", "x86_64")
	seedView(t, s, "dev-vms")
	must(t, s.UpsertWorkflow(ctx, types.Workflow{Name: "install", Steps: []types.Step{{Name: "go", ViewName: "dev-vms"}}}))
	must(t, s.UpsertIntent(ctx, types.Intent{Name: "chrome", Kind: types.IntentApplication}))
	must(t, s.UpsertIntent(ctx, types.Intent{Name: "firefox", Kind: types.IntentApplication}))
	// Two distinct Blueprints both manage the fresh namespace app.managed.
	must(t, s.UpsertBlueprint(ctx, ownedBlueprint("bp-a", 1, "app.managed")))
	must(t, s.UpsertBlueprint(ctx, ownedBlueprint("bp-b", 1, "app.managed")))
	must(t, s.UpsertAssignment(ctx, types.Assignment{Name: "asgA", Intent: "chrome", View: "dev-vms", Blueprint: "bp-a", BlueprintVersion: 1}))
	must(t, s.UpsertAssignment(ctx, types.Assignment{Name: "asgB", Intent: "firefox", View: "dev-vms", Blueprint: "bp-b", BlueprintVersion: 1}))

	plan := compileApply(t, s, 0)
	found := false
	for _, e := range plan.Errors {
		if contains(e, "claimed by multiple Blueprints") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a blueprint ownership conflict, got %v", plan.Errors)
	}
	// Neither claimant's baseline is applied, and the namespace stays unowned.
	if _, ok, _ := s.GetFacetOwner(ctx, "app.managed"); ok {
		t.Fatal("a contested namespace must not be registered to either blueprint")
	}
}

func TestCompileMaxDeltaPauseAndAck(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// Seed a large stable set, compile once to store the snapshot.
	for i := 0; i < 10; i++ {
		seedEntity(t, s, fmt.Sprintf("u%d", i), "x86_64")
	}
	seedView(t, s, "dev-vms")
	must(t, s.UpsertIntent(ctx, types.Intent{Name: "chrome", Kind: types.IntentApplication}))
	must(t, s.UpsertBlueprint(ctx, appBlueprint("application", 1, types.ClaimAdditive)))
	must(t, s.UpsertAssignment(ctx, types.Assignment{Name: "kiosks", Intent: "chrome", View: "dev-vms", Blueprint: "application", BlueprintVersion: 1}))
	compileApply(t, s, 0.5)
	m, _, _ := s.GetAssignmentMembership(ctx, "kiosks")
	if m.MemberCount != 10 {
		t.Fatalf("first compile membership: %d", m.MemberCount)
	}

	// Add 6 entities (>50% delta) → pause.
	for i := 10; i < 16; i++ {
		seedEntity(t, s, fmt.Sprintf("u%d", i), "x86_64")
	}
	plan := compileApply(t, s, 0.5)
	var paused bool
	for _, d := range plan.Deltas {
		if d.Assignment == "kiosks" && d.Paused {
			paused = true
		}
	}
	if !paused {
		t.Fatalf("expected max-delta pause, deltas: %+v", plan.Deltas)
	}
	// Snapshot unchanged while paused.
	m, _, _ = s.GetAssignmentMembership(ctx, "kiosks")
	if m.MemberCount != 10 {
		t.Fatalf("paused compile must not update the snapshot: %d", m.MemberCount)
	}

	// Bump ackDelta → the over-threshold compile applies.
	must(t, s.UpsertAssignment(ctx, types.Assignment{Name: "kiosks", Intent: "chrome", View: "dev-vms", Blueprint: "application", BlueprintVersion: 1, AckDelta: 1}))
	compileApply(t, s, 0.5)
	m, _, _ = s.GetAssignmentMembership(ctx, "kiosks")
	if m.MemberCount != 16 || m.AckedDelta != 1 {
		t.Fatalf("ack should apply the delta: count=%d acked=%d", m.MemberCount, m.AckedDelta)
	}
}

func TestCompileOrphanOnWithdrawal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	seedEntity(t, s, "u1", "x86_64")
	seedView(t, s, "dev-vms")
	must(t, s.UpsertIntent(ctx, types.Intent{Name: "chrome", Kind: types.IntentApplication}))
	must(t, s.UpsertBlueprint(ctx, appBlueprint("application", 1, types.ClaimAdditive)))
	must(t, s.UpsertAssignment(ctx, types.Assignment{Name: "kiosks", Intent: "chrome", View: "dev-vms", Blueprint: "application", BlueprintVersion: 1}))
	compileApply(t, s, 0)
	name := CompiledName("kiosks", "application", 1, 0)
	if _, err := s.GetBaseline(ctx, name); err != nil {
		t.Fatalf("baseline should exist: %v", err)
	}

	// Withdraw the Assignment (onRemove=retain) → orphan Finding + prune.
	must(t, s.DeleteAssignment(ctx, "kiosks"))
	compileApply(t, s, 0)
	if _, err := s.GetBaseline(ctx, name); err == nil {
		t.Fatal("withdrawn assignment's compiled baseline must be pruned")
	}
	findings, _ := s.ListFindings(ctx, name, "", 0)
	if len(findings) != 1 || findings[0].Framework != "orphan" || findings[0].Status != types.FindingOpen {
		t.Fatalf("expected one open orphan finding, got %+v", findings)
	}
	if _, ok, _ := s.GetAssignmentMembership(ctx, "kiosks"); ok {
		t.Fatal("withdrawn assignment's membership snapshot must be dropped")
	}
}

// TestCompileOnRemoveRevoke proves onRemove=remove surfaces the Blueprint's
// remove Workflow on the orphan Finding (ADR-0030) — a ref for the operator to
// launch (§5 Flow 2), never auto-run. The Intent stays declared; the Assignment
// is withdrawn.
func TestCompileOnRemoveRevoke(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	seedEntity(t, s, "u1", "x86_64")
	seedView(t, s, "dev-vms")
	must(t, s.UpsertWorkflow(ctx, types.Workflow{Name: "cert-revoke", Steps: []types.Step{{Name: "revoke", ViewName: "dev-vms", Actuator: "cert-issuer"}}}))
	must(t, s.UpsertIntent(ctx, types.Intent{Name: "web-cert", Kind: types.IntentCertificate, OnRemove: types.OnRemoveRemove}))
	bp := appBlueprint("certificate", 1, types.ClaimAdditive)
	bp.For = types.IntentCertificate
	bp.RemoveWorkflow = "cert-revoke"
	must(t, s.UpsertBlueprint(ctx, bp))
	must(t, s.UpsertAssignment(ctx, types.Assignment{Name: "site-certs", Intent: "web-cert", View: "dev-vms", Blueprint: "certificate", BlueprintVersion: 1}))
	compileApply(t, s, 0)
	name := CompiledName("site-certs", "certificate", 1, 0)
	if _, err := s.GetBaseline(ctx, name); err != nil {
		t.Fatalf("baseline should exist: %v", err)
	}

	// Withdraw the Assignment; the Intent (onRemove=remove) stays declared.
	must(t, s.DeleteAssignment(ctx, "site-certs"))
	compileApply(t, s, 0)
	findings, _ := s.ListFindings(ctx, name, "", 0)
	if len(findings) != 1 || findings[0].Status != types.FindingOpen {
		t.Fatalf("expected one open orphan finding, got %+v", findings)
	}
	var d map[string]any
	if err := json.Unmarshal(findings[0].Diff, &d); err != nil {
		t.Fatalf("orphan detail: %v", err)
	}
	if d["onRemove"] != "remove" || d["removeWorkflow"] != "cert-revoke" {
		t.Fatalf("orphan must carry the revoke remediation ref, got %v", d)
	}
}

func TestCompileRejectsNonCacView(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	seedEntity(t, s, "u1", "x86_64")
	// api-declared View — an Assignment may not target it.
	if _, err := s.DeclareViewAs(ctx, "api-vms", types.ViewSelector{Kinds: []string{"vm"}}, graph.DeclaredByAPI); err != nil {
		t.Fatal(err)
	}
	must(t, s.UpsertIntent(ctx, types.Intent{Name: "chrome", Kind: types.IntentApplication}))
	must(t, s.UpsertBlueprint(ctx, appBlueprint("application", 1, types.ClaimAdditive)))
	must(t, s.UpsertAssignment(ctx, types.Assignment{Name: "kiosks", Intent: "chrome", View: "api-vms", Blueprint: "application", BlueprintVersion: 1}))
	plan := compileApply(t, s, 0)
	found := false
	for _, e := range plan.Errors {
		if contains(e, "not cac-declared") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a cac-View rejection, got %v", plan.Errors)
	}
}

func TestCompileRejectsParametrizedView(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	seedEntity(t, s, "u1", "x86_64")
	// A parametrized cac View — binds only at launch, not as a compile target.
	if _, err := s.DeclareViewAs(ctx, "param-vms",
		types.ViewSelector{Kinds: []string{"vm"}, Labels: map[string]string{"host": "{{.param.host}}"}},
		graph.DeclaredByCaC); err != nil {
		t.Fatal(err)
	}
	must(t, s.UpsertIntent(ctx, types.Intent{Name: "chrome", Kind: types.IntentApplication}))
	must(t, s.UpsertBlueprint(ctx, appBlueprint("application", 1, types.ClaimAdditive)))
	must(t, s.UpsertAssignment(ctx, types.Assignment{Name: "kiosks", Intent: "chrome", View: "param-vms", Blueprint: "application", BlueprintVersion: 1}))
	plan := compileApply(t, s, 0)
	found := false
	for _, e := range plan.Errors {
		if contains(e, "parametrized") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a parametrized-View rejection, got %v", plan.Errors)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && indexOf(s, sub) >= 0))
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
