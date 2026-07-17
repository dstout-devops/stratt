package desiredstate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func writeBaseline(t *testing.T, root, file, content string) {
	t.Helper()
	dir := filepath.Join(root, "baselines")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseBaselines(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "all-vms.yaml", "name: all-vms\nselector: {kinds: [vm]}\n")
	writeBaseline(t, root, "kernel.yaml", `
name: kernel-drift
viewName: all-vms
actuator: ansible
cron: "0 * * * *"
severity: warning
dampingObservations: 2
remediationWorkflow: patch-dev
framework: cis
`)
	parsed, err := ParseDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Baselines) != 1 {
		t.Fatalf("baselines: %+v", parsed.Baselines)
	}
	b := parsed.Baselines[0]
	if b.Name != "kernel-drift" || b.DampingObservations != 2 || b.Framework != "cis" ||
		b.RemediationWorkflow != "patch-dev" || b.Severity != types.SeverityWarning {
		t.Fatalf("baseline: %+v", b)
	}

	// Rejections: structural invariants only. Baseline VALIDATION is now content-blind
	// (ADR-0046): it no longer switches on tool name nor polices tool-specific params
	// (a declared params.check is inert; a non-read-only actuator is rejected at LAUNCH
	// by the DryRunnable capability gate, not here).
	for name, doc := range map[string]string{
		"missing cron":       "name: x\nviewName: v\nseverity: info\n",
		"missing view":       "name: x\ncron: '* * * * *'\nseverity: info\n",
		"bad severity":       "name: x\nviewName: v\ncron: '* * * * *'\nseverity: urgent\n",
		"negative damping":   "name: x\nviewName: v\ncron: '* * * * *'\nseverity: info\ndampingObservations: -1\n",
		"creds no principal": "name: x\nviewName: v\ncron: '* * * * *'\nseverity: info\ncredentialRefs: [c]\n",
	} {
		bad := t.TempDir()
		writeDecl(t, bad, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
		writeBaseline(t, bad, "x.yaml", doc)
		if _, err := ParseDir(bad); err == nil {
			t.Fatalf("invalid baseline (%s) must be rejected", name)
		}
	}

	// Content-blind acceptance: a declared (inert) params.check no longer trips a
	// tool-name switch — validation names no tool. (The §1.5 params-Contract seam
	// STAYS — a non-ansible actuator's params are still validated against its pinned
	// input Contract, ADR-0046 finding #3 — so it is not asserted here.)
	okDir := t.TempDir()
	writeDecl(t, okDir, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
	writeBaseline(t, okDir, "x.yaml", "name: x\nviewName: v\nactuator: ansible\ncron: '* * * * *'\nseverity: info\nparams: {check: false}\n")
	if _, err := ParseDir(okDir); err != nil {
		t.Fatalf("content-blind validation must accept an inert declared params.check, got %v", err)
	}

	// baselines/ absent → valid (repos predating ADR-0019).
	old := t.TempDir()
	writeDecl(t, old, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
	if _, err := ParseDir(old); err != nil {
		t.Fatalf("absent baselines/ must be valid: %v", err)
	}
}

// TestParseFacetObservationBaseline covers the hand-written facet-observation
// Baseline (ADR-0033): mode + expected values, no actuator/params/check Step.
func TestParseFacetObservationBaseline(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "linux.yaml", "name: linux\nselector: {facets: [{namespace: os.kernel, path: family, equals: linux}]}\n")
	writeBaseline(t, root, "cis-sshd.yaml", `
name: cis-5-2-8-permit-root-login
viewName: linux
mode: facet-observation
cron: "0 * * * *"
severity: critical
framework: cis
expected:
  - namespace: os.hardening.sshd
    path: permit_root_login
    equals: "no"
  - namespace: os.hardening.auditd
    path: running
    equals: true
`)
	parsed, err := ParseDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Baselines) != 1 {
		t.Fatalf("baselines: %+v", parsed.Baselines)
	}
	b := parsed.Baselines[0]
	if b.Mode != types.FacetObservation || b.Framework != "cis" {
		t.Fatalf("baseline: %+v", b)
	}
	if len(b.Expected) != 2 {
		t.Fatalf("expected: %+v", b.Expected)
	}
	if b.Expected[0].Namespace != "os.hardening.sshd" || string(b.Expected[0].Equals) != `"no"` {
		t.Fatalf("expected[0]: %+v (%s)", b.Expected[0], b.Expected[0].Equals)
	}
	if string(b.Expected[1].Equals) != `true` {
		t.Fatalf("expected[1] equals: %s", b.Expected[1].Equals)
	}

	// Rejections specific to facet-observation.
	for name, doc := range map[string]string{
		"no expected":       "name: x\nviewName: v\nmode: facet-observation\ncron: '* * * * *'\nseverity: info\n",
		"actuator set":      "name: x\nviewName: v\nmode: facet-observation\ncron: '* * * * *'\nseverity: info\nactuator: ansible\nexpected: [{namespace: n, equals: 1}]\n",
		"params set":        "name: x\nviewName: v\nmode: facet-observation\ncron: '* * * * *'\nseverity: info\nparams: {x: 1}\nexpected: [{namespace: n, equals: 1}]\n",
		"creds set":         "name: x\nviewName: v\nmode: facet-observation\ncron: '* * * * *'\nseverity: info\ncredentialRefs: [c]\nprincipal: p\nexpected: [{namespace: n, equals: 1}]\n",
		"no matcher":        "name: x\nviewName: v\nmode: facet-observation\ncron: '* * * * *'\nseverity: info\nexpected: [{namespace: n}]\n",
		"two matchers":      "name: x\nviewName: v\nmode: facet-observation\ncron: '* * * * *'\nseverity: info\nexpected: [{namespace: n, equals: 1, contains: 2}]\n",
		"empty namespace":   "name: x\nviewName: v\nmode: facet-observation\ncron: '* * * * *'\nseverity: info\nexpected: [{equals: 1}]\n",
		"missing view":      "name: x\nmode: facet-observation\ncron: '* * * * *'\nseverity: info\nexpected: [{namespace: n, equals: 1}]\n",
		"claim not a field": "name: x\nviewName: v\nmode: facet-observation\ncron: '* * * * *'\nseverity: info\nclaim: exclusive\nexpected: [{namespace: n, equals: 1}]\n",
		"unknown mode":      "name: x\nviewName: v\nmode: probe\ncron: '* * * * *'\nseverity: info\n",
	} {
		bad := t.TempDir()
		writeDecl(t, bad, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
		writeBaseline(t, bad, "x.yaml", doc)
		if _, err := ParseDir(bad); err == nil {
			t.Fatalf("invalid facet-observation baseline (%s) must be rejected", name)
		}
	}
}

func TestBaselinePlanApplyLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	b := types.Baseline{
		Name: "kernel-drift", ViewName: "all-vms", Cron: "0 * * * *",
		Severity: types.SeverityInfo,
	}
	decls := Declarations{Baselines: []types.Baseline{b}}

	plan, err := ComputePlan(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	if got := entryFor(plan, KindBaseline, "kernel-drift"); got == nil || got.Action != ActionCreate {
		t.Fatalf("want create, got %+v", plan.Entries)
	}
	if _, err := Apply(ctx, s, decls); err != nil {
		t.Fatal(err)
	}

	// Same declaration → noop.
	plan, err = ComputePlan(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	if got := entryFor(plan, KindBaseline, "kernel-drift"); got == nil || got.Action != ActionNoop {
		t.Fatalf("want noop, got %+v", plan.Entries)
	}

	// Changed cadence → update.
	decls.Baselines[0].Cron = "@hourly"
	plan, err = ComputePlan(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	if got := entryFor(plan, KindBaseline, "kernel-drift"); got == nil || got.Action != ActionUpdate {
		t.Fatalf("want update, got %+v", plan.Entries)
	}
	if _, err := Apply(ctx, s, decls); err != nil {
		t.Fatal(err)
	}

	// Undeclared → prune.
	realized, err := Apply(ctx, s, Declarations{})
	if err != nil {
		t.Fatal(err)
	}
	if got := entryFor(realized, KindBaseline, "kernel-drift"); got == nil || got.Action != ActionDelete || got.Error != "" {
		t.Fatalf("want delete, got %+v", realized.Entries)
	}
	if _, err := s.GetBaseline(ctx, "kernel-drift"); err == nil {
		t.Fatalf("baseline must be pruned")
	}
}

func entryFor(p Plan, kind, name string) *PlanEntry {
	for i := range p.Entries {
		if p.Entries[i].Kind == kind && p.Entries[i].Name == name {
			return &p.Entries[i]
		}
	}
	return nil
}
