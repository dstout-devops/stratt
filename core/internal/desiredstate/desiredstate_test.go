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

	parsed, err := ParseDir(root)
	if err != nil {
		t.Fatal(err)
	}
	decls := parsed.Views
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
	plan, err := ComputePlan(ctx, s, Declarations{Views: decls})
	if err != nil {
		t.Fatal(err)
	}
	got := actionsByName(plan)
	if got["adoptme"] != ActionAdopt || got["fresh"] != ActionCreate {
		t.Fatalf("plan: %+v", got)
	}

	applied, err := Apply(ctx, s, Declarations{Views: decls})
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
	plan2, err := Apply(ctx, s, Declarations{Views: decls})
	if err != nil {
		t.Fatal(err)
	}
	if plan2.Changes() != 0 {
		t.Fatalf("re-apply should be all noop: %+v", plan2.Entries)
	}

	// Selector change → update, version bump.
	decls[1].Selector = sel("vm", "host")
	plan3, err := Apply(ctx, s, Declarations{Views: decls})
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
	plan4, err := Apply(ctx, s, Declarations{Views: decls[:1]}) // only adoptme remains declared
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
	plan5, err := Apply(ctx, s, Declarations{Views: decls2})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range plan5.Entries {
		if e.Error != "" {
			t.Fatalf("re-declare after prune failed on %s: %s", e.Name, e.Error)
		}
	}
}

func writeCredRef(t *testing.T, root, file, content string) {
	t.Helper()
	dir := filepath.Join(root, "credential-refs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseCredentialRefs(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "all-vms.yaml", "name: all-vms\nselector: {kinds: [vm]}\n")
	writeCredRef(t, root, "vc.yaml", `
name: vcenter-dev
ownerTeam: platform
backend: k8s-secret
locator: { namespace: default, name: vcenter-dev }
injection:
  - { key: password, as: env, name: VC_PASS }
  - { key: username, as: file, name: user }
`)
	parsed, err := ParseDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.CredentialRefs) != 1 {
		t.Fatalf("refs: %+v", parsed.CredentialRefs)
	}
	ref := parsed.CredentialRefs[0]
	if ref.Name != "vcenter-dev" || ref.OwnerTeam != "platform" || ref.Backend != types.BackendK8sSecret {
		t.Fatalf("ref: %+v", ref)
	}
	if len(ref.Injection) != 2 || ref.Injection[0].As != types.InjectEnv || ref.Injection[1].As != types.InjectFile {
		t.Fatalf("injection: %+v", ref.Injection)
	}

	// Rejections: bad backend, bad injection mode, missing injection.
	for name, doc := range map[string]string{
		"backend":   "name: x\nownerTeam: t\nbackend: sqlite\nlocator: {name: n}\ninjection: [{key: k, as: env, name: N}]\n",
		"mode":      "name: x\nownerTeam: t\nbackend: k8s-secret\nlocator: {name: n}\ninjection: [{key: k, as: extra_vars, name: N}]\n",
		"injection": "name: x\nownerTeam: t\nbackend: k8s-secret\nlocator: {name: n}\ninjection: []\n",
	} {
		bad := t.TempDir()
		writeDecl(t, bad, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
		writeCredRef(t, bad, "x.yaml", doc)
		if _, err := ParseDir(bad); err == nil {
			t.Fatalf("invalid %s must be rejected", name)
		}
	}

	// credential-refs/ absent → valid (pre-ADR-0009 repos).
	old := t.TempDir()
	writeDecl(t, old, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
	if _, err := ParseDir(old); err != nil {
		t.Fatalf("missing credential-refs dir must be fine: %v", err)
	}
}

func TestCredentialRefLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	mkRef := func(team string) types.CredentialRef {
		return types.CredentialRef{
			Name: "vc", OwnerTeam: team, Backend: types.BackendK8sSecret,
			Locator:   json.RawMessage(`{"name":"vc-secret"}`),
			Injection: []types.CredentialInjection{{Key: "password", As: types.InjectEnv, Name: "VC_PASS"}},
		}
	}
	decls := Declarations{CredentialRefs: []types.CredentialRef{mkRef("platform")}}

	plan, err := Apply(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	if actionsByName(plan)["vc"] != ActionCreate {
		t.Fatalf("plan: %+v", plan.Entries)
	}

	// Noop re-apply; update on change.
	plan, _ = Apply(ctx, s, decls)
	if plan.Changes() != 0 {
		t.Fatalf("re-apply should be noop: %+v", plan.Entries)
	}
	decls.CredentialRefs[0] = mkRef("other-team")
	plan, _ = Apply(ctx, s, decls)
	if actionsByName(plan)["vc"] != ActionUpdate {
		t.Fatalf("owner change should plan update: %+v", plan.Entries)
	}

	// api may not modify a cac ref.
	if _, err := s.DeclareCredentialRefAs(ctx, mkRef("hijack"), graph.DeclaredByAPI); err == nil || !strings.Contains(err.Error(), "cac") {
		t.Fatalf("api declare on cac ref must fail, got %v", err)
	}

	// Prune.
	plan, _ = Apply(ctx, s, Declarations{})
	if actionsByName(plan)["vc"] != ActionDelete {
		t.Fatalf("undeclared ref should prune: %+v", plan.Entries)
	}
	if _, err := s.GetCredentialRef(ctx, "vc"); err == nil {
		t.Fatal("pruned ref should be gone")
	}
}

func TestPruneStats(t *testing.T) {
	p := Plan{Entries: []PlanEntry{
		{Kind: KindView, Name: "a", Action: ActionNoop},
		{Kind: KindView, Name: "b", Action: ActionUpdate},
		{Kind: KindView, Name: "c", Action: ActionDelete},
		{Kind: KindView, Name: "d", Action: ActionDelete},
		{Kind: KindView, Name: "e", Action: ActionCreate}, // not a current cac view
		{Kind: KindView, Name: "f", Action: ActionAdopt},  // currently api, not cac
		// One kind's bulk must not mask another's total disappearance:
		// every declared credential-ref pruned.
		{Kind: KindCredentialRef, Name: "x", Action: ActionDelete},
	}}
	stats := p.PruneStats()
	if v := stats[KindView]; v[0] != 2 || v[1] != 4 {
		t.Fatalf("view prune stats: %v", v)
	}
	if c := stats[KindCredentialRef]; c[0] != 1 || c[1] != 1 {
		t.Fatalf("credential-ref prune stats: %v", c)
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

// ── Triggers (ADR-0010) ──────────────────────────────────────────────────────

func writeTrigger(t *testing.T, root, file, content string) {
	t.Helper()
	dir := filepath.Join(root, "triggers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseTriggers(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "all-vms.yaml", "name: all-vms\nselector: {kinds: [vm]}\n")
	writeTrigger(t, root, "nightly.yaml", `
name: nightly-facts
cron: "0 2 * * *"
viewName: all-vms
actuator: ansible
credentialRefs: [vcenter-dev]
principal: "381351939796919559"
`)
	parsed, err := ParseDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Triggers) != 1 {
		t.Fatalf("triggers: %+v", parsed.Triggers)
	}
	tr := parsed.Triggers[0]
	if tr.Kind != types.TriggerSchedule {
		t.Fatalf("kind must default to schedule, got %q", tr.Kind)
	}
	if tr.Name != "nightly-facts" || tr.Cron != "0 2 * * *" || tr.Principal == "" {
		t.Fatalf("trigger: %+v", tr)
	}

	// Rejections: missing cron, unknown kind, credentialRefs without a
	// principal (could never pass the dispatch-time use check, §2.5).
	for name, doc := range map[string]string{
		"cron":      "name: x\nviewName: v\n",
		"kind":      "name: x\nkind: webhook\ncron: '* * * * *'\nviewName: v\n",
		"principal": "name: x\ncron: '* * * * *'\nviewName: v\ncredentialRefs: [c]\n",
		"slices":    "name: x\ncron: '* * * * *'\nviewName: v\nslices: -1\n",
	} {
		bad := t.TempDir()
		writeDecl(t, bad, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
		writeTrigger(t, bad, "x.yaml", doc)
		if _, err := ParseDir(bad); err == nil {
			t.Fatalf("invalid %s must be rejected", name)
		}
	}

	// triggers/ absent → valid (repos predating ADR-0010).
	old := t.TempDir()
	writeDecl(t, old, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
	if _, err := ParseDir(old); err != nil {
		t.Fatalf("absent triggers/ must be valid: %v", err)
	}
}

func TestTriggerPlanApplyLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	trig := types.Trigger{
		Name: "nightly", Kind: types.TriggerSchedule, Cron: "0 2 * * *",
		ViewName: "all-vms",
	}
	decls := Declarations{Triggers: []types.Trigger{trig}}

	plan, err := Apply(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	if actionsByName(plan)["nightly"] != ActionCreate {
		t.Fatalf("plan: %+v", plan.Entries)
	}

	// Re-apply: noop (semantic equality of the declaration document).
	plan2, err := Apply(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	if plan2.Changes() != 0 {
		t.Fatalf("re-apply should be all noop: %+v", plan2.Entries)
	}

	// Changed cron → update, round-trips through the store.
	decls.Triggers[0].Cron = "0 3 * * *"
	decls.Triggers[0].Paused = true
	plan3, err := Apply(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	if actionsByName(plan3)["nightly"] != ActionUpdate {
		t.Fatalf("changed cron should plan update: %+v", plan3.Entries)
	}
	got, err := s.GetTrigger(ctx, "nightly")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cron != "0 3 * * *" || !got.Paused {
		t.Fatalf("trigger round-trip: %+v", got)
	}

	// Prune: an undeclared trigger is deleted, and prune stats see the kind.
	empty := Declarations{}
	prunePlan, err := ComputePlan(ctx, s, empty)
	if err != nil {
		t.Fatal(err)
	}
	if st := prunePlan.PruneStats()[KindTrigger]; st[0] != 1 || st[1] != 1 {
		t.Fatalf("trigger prune stats: %v", st)
	}
	if _, err := Apply(ctx, s, empty); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTrigger(ctx, "nightly"); err == nil {
		t.Fatal("pruned trigger should be gone")
	}
	if ts, err := s.ListTriggers(ctx); err != nil || len(ts) != 0 {
		t.Fatalf("list after prune: %v %v", ts, err)
	}
}

// ── Workflows (ADR-0011) ─────────────────────────────────────────────────────

func writeWorkflow(t *testing.T, root, file, content string) {
	t.Helper()
	dir := filepath.Join(root, "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseWorkflows(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "all-vms.yaml", "name: all-vms\nselector: {kinds: [vm]}\n")
	writeWorkflow(t, root, "patch.yaml", `
name: patch-dev
steps:
  - name: gather
    viewName: all-vms
    actuator: ansible
    credentialRefs: [vcenter-dev]
  - name: approve
    needs: [gather]
    gate:
      approvers:
        teams: [platform]
      timeoutSeconds: 3600
  - name: report
    needs: [approve]
    viewName: all-vms
    actuator: script
    params: { script: "echo done" }
  - name: cleanup
    needs: [gather, approve]
    when: failure
    viewName: all-vms
    actuator: script
    params: { script: "echo cleanup" }
`)
	parsed, err := ParseDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Workflows) != 1 || len(parsed.Workflows[0].Steps) != 4 {
		t.Fatalf("workflows: %+v", parsed.Workflows)
	}
	w := parsed.Workflows[0]
	if w.Steps[1].Gate == nil || w.Steps[1].Gate.Approvers.Teams[0] != "platform" || w.Steps[1].Gate.TimeoutSeconds != 3600 {
		t.Fatalf("gate step: %+v", w.Steps[1])
	}
	if w.Steps[3].When != types.WhenFailure {
		t.Fatalf("when: %+v", w.Steps[3])
	}

	// Rejections.
	for name, doc := range map[string]string{
		"cycle":          "name: w\nsteps:\n  - {name: a, needs: [b], viewName: v}\n  - {name: b, needs: [a], viewName: v}\n",
		"unknown-need":   "name: w\nsteps:\n  - {name: a, needs: [nope], viewName: v}\n",
		"self-need":      "name: w\nsteps:\n  - {name: a, needs: [a], viewName: v}\n",
		"dup-step":       "name: w\nsteps:\n  - {name: a, viewName: v}\n  - {name: a, viewName: v}\n",
		"gate+actuation": "name: w\nsteps:\n  - {name: a, viewName: v, gate: {approvers: {teams: [t]}}}\n",
		"no-view":        "name: w\nsteps:\n  - {name: a, actuator: script}\n",
		"no-approvers":   "name: w\nsteps:\n  - {name: a, gate: {approvers: {}}}\n",
		"bad-when":       "name: w\nsteps:\n  - {name: a, viewName: v}\n  - {name: b, needs: [a], when: sometimes, viewName: v}\n",
		"when-no-needs":  "name: w\nsteps:\n  - {name: a, when: failure, viewName: v}\n",
		"no-steps":       "name: w\nsteps: []\n",
		// Action shape (ADR-0031): an action step is targetless.
		"action+view":    "name: w\nsteps:\n  - {name: a, action: certissuer/revoke, viewName: v, params: {addr: 'http://x', serial: 'a:b'}, credentialRefs: [c]}\n",
		"action+gate":    "name: w\nsteps:\n  - {name: a, action: certissuer/revoke, gate: {approvers: {teams: [t]}}}\n",
		"bad-action-in":  "name: w\nsteps:\n  - {name: a, action: certissuer/revoke, params: {addr: 'http://x'}}\n", // missing serial
		"unknown-action": "name: w\nsteps:\n  - {name: a, action: certissuer/nope, params: {}}\n",
	} {
		bad := t.TempDir()
		writeDecl(t, bad, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
		writeWorkflow(t, bad, "x.yaml", doc)
		if _, err := ParseDir(bad); err == nil {
			t.Fatalf("invalid %s must be rejected", name)
		}
	}

	// A valid Action Step parses (targetless typed operation, ADR-0031).
	act := t.TempDir()
	writeDecl(t, act, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
	writeWorkflow(t, act, "cert.yaml",
		"name: cert-revoke\nsteps:\n  - name: revoke\n    action: certissuer/revoke\n    credentialRefs: [cert-issuer]\n    params: {addr: 'http://x', serial: 'a:b'}\n")
	parsedAct, err := ParseDir(act)
	if err != nil {
		t.Fatalf("valid action workflow must parse: %v", err)
	}
	if s := parsedAct.Workflows[0].Steps[0]; s.Action != "certissuer/revoke" || s.ViewName != "" {
		t.Fatalf("action step: %+v", s)
	}

	// Cross-Step output binding ({{.steps.x.outputs.y}}) is a valid namespace
	// on a downstream Step's params (ADR-0031).
	bind := t.TempDir()
	writeDecl(t, bind, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
	writeWorkflow(t, bind, "seam.yaml",
		"name: seam\nsteps:\n"+
			"  - {name: provision, action: awsec2/create-vm, params: {region: us-east-1, ami: ami-1}}\n"+
			"  - name: configure\n    needs: [provision]\n    viewName: v\n    actuator: script\n    params: {script: '{{.steps.provision.outputs.instanceId}}'}\n")
	if _, err := ParseDir(bind); err != nil {
		t.Fatalf("steps-namespace binding must parse: %v", err)
	}

	// workflows/ absent → valid (repos predating ADR-0011).
	old := t.TempDir()
	writeDecl(t, old, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
	if _, err := ParseDir(old); err != nil {
		t.Fatalf("absent workflows/ must be valid: %v", err)
	}
}

func TestWorkflowPlanApplyLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	wf := types.Workflow{Name: "patch", Steps: []types.Step{
		{Name: "gather", ViewName: "all-vms"},
		{Name: "approve", Needs: []string{"gather"}, Gate: &types.GateSpec{
			Approvers: types.GateApprovers{Teams: []string{"platform"}},
		}},
	}}
	decls := Declarations{Workflows: []types.Workflow{wf}}

	plan, err := Apply(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	if actionsByName(plan)["patch"] != ActionCreate {
		t.Fatalf("plan: %+v", plan.Entries)
	}

	plan2, err := Apply(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	if plan2.Changes() != 0 {
		t.Fatalf("re-apply should be all noop: %+v", plan2.Entries)
	}

	decls.Workflows[0].Steps[0].Actuator = "script"
	plan3, err := Apply(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	if actionsByName(plan3)["patch"] != ActionUpdate {
		t.Fatalf("changed step should plan update: %+v", plan3.Entries)
	}
	got, err := s.GetWorkflow(ctx, "patch")
	if err != nil {
		t.Fatal(err)
	}
	if got.Steps[0].Actuator != "script" || got.Steps[1].Gate == nil {
		t.Fatalf("workflow round-trip: %+v", got)
	}

	prunePlan, err := ComputePlan(ctx, s, Declarations{})
	if err != nil {
		t.Fatal(err)
	}
	if st := prunePlan.PruneStats()[KindWorkflow]; st[0] != 1 || st[1] != 1 {
		t.Fatalf("workflow prune stats: %v", st)
	}
	if _, err := Apply(ctx, s, Declarations{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetWorkflow(ctx, "patch"); err == nil {
		t.Fatal("pruned workflow should be gone")
	}
}
