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

	// Rejections: the check must be read-only by declaration — check is the
	// platform's flag, opentofu only ever plans, and only Actuators with
	// check semantics are accepted (ADR-0019).
	for name, doc := range map[string]string{
		"missing cron":       "name: x\nviewName: v\nseverity: info\n",
		"missing view":       "name: x\ncron: '* * * * *'\nseverity: info\n",
		"bad severity":       "name: x\nviewName: v\ncron: '* * * * *'\nseverity: urgent\n",
		"declared check":     "name: x\nviewName: v\ncron: '* * * * *'\nseverity: info\nparams: {check: false}\n",
		"tofu apply":         "name: x\nviewName: v\ncron: '* * * * *'\nseverity: info\nactuator: opentofu\nparams: {mode: apply, module: m, workspace: w}\n",
		"no check semantics": "name: x\nviewName: v\ncron: '* * * * *'\nseverity: info\nactuator: script\n",
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

	// baselines/ absent → valid (repos predating ADR-0019).
	old := t.TempDir()
	writeDecl(t, old, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
	if _, err := ParseDir(old); err != nil {
		t.Fatalf("absent baselines/ must be valid: %v", err)
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
