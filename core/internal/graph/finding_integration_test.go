package graph

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func testBaseline(damping int) types.Baseline {
	return types.Baseline{
		Name: "kernel-drift", ViewName: "dev-vms", Cron: "@hourly",
		Severity: types.SeverityWarning, Framework: "cis",
		DampingObservations: damping,
	}
}

func mustRun(t *testing.T, store *Store) string {
	t.Helper()
	run, err := store.CreateRun(context.Background(), types.Run{WorkflowID: "wf-test", Baseline: "kernel-drift"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return run.ID
}

func liveFinding(t *testing.T, store *Store, baseline, target string) *types.Finding {
	t.Helper()
	fs, err := store.ListFindings(context.Background(), baseline, "", 0)
	if err != nil {
		t.Fatalf("list findings: %v", err)
	}
	for i := range fs {
		if fs[i].Target == target && fs[i].Status != types.FindingResolved {
			return &fs[i]
		}
	}
	return nil
}

func TestFindingDampingImmediate(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	b := testBaseline(1)
	runID := mustRun(t, store)

	out, err := store.RecordBaselineObservations(ctx, b, runID, map[string]BaselineObservation{
		"vm-1": {Drifted: true, EntityID: "ent-1", Detail: json.RawMessage(`[{"task":"sysctl"}]`)},
		"vm-2": {Drifted: false},
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if out.Opened != 1 || out.Pending != 0 {
		t.Fatalf("want 1 opened, got %+v", out)
	}
	f := liveFinding(t, store, b.Name, "vm-1")
	if f == nil || f.Status != types.FindingOpen {
		t.Fatalf("want open finding for vm-1, got %+v", f)
	}
	if f.Severity != types.SeverityWarning || f.Framework != "cis" || f.EntityID != "ent-1" || f.RunID != runID {
		t.Fatalf("finding not stamped from baseline/run: %+v", f)
	}
	if f.OpenedAt == nil || len(f.Diff) == 0 {
		t.Fatalf("open finding missing openedAt/diff: %+v", f)
	}
	if got := liveFinding(t, store, b.Name, "vm-2"); got != nil {
		t.Fatalf("clean target must not create a finding, got %+v", got)
	}
}

func TestFindingDampingThreshold(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	b := testBaseline(3)
	drift := map[string]BaselineObservation{"vm-1": {Drifted: true}}

	// Two drifted observations: pending, never open.
	for i := 0; i < 2; i++ {
		out, err := store.RecordBaselineObservations(ctx, b, mustRun(t, store), drift)
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if out.Opened != 0 || out.Pending != 1 {
			t.Fatalf("observation %d: want pending, got %+v", i, out)
		}
	}
	f := liveFinding(t, store, b.Name, "vm-1")
	if f == nil || f.Status != types.FindingPending || f.ConsecutiveDrifted != 2 {
		t.Fatalf("want pending count 2, got %+v", f)
	}

	// Third consecutive: opens.
	out, err := store.RecordBaselineObservations(ctx, b, mustRun(t, store), drift)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if out.Opened != 1 {
		t.Fatalf("want opened, got %+v", out)
	}
	if f = liveFinding(t, store, b.Name, "vm-1"); f == nil || f.Status != types.FindingOpen || f.ConsecutiveDrifted != 3 {
		t.Fatalf("want open count 3, got %+v", f)
	}
}

func TestFindingFlapAbsorbedAndResolve(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	b := testBaseline(2)
	drift := map[string]BaselineObservation{"vm-1": {Drifted: true}}
	clean := map[string]BaselineObservation{"vm-1": {Drifted: false}}

	// drift → clean: the pending row is deleted, nothing ever fired.
	if _, err := store.RecordBaselineObservations(ctx, b, mustRun(t, store), drift); err != nil {
		t.Fatalf("record: %v", err)
	}
	out, err := store.RecordBaselineObservations(ctx, b, mustRun(t, store), clean)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if out.Cleared != 1 {
		t.Fatalf("want flap cleared, got %+v", out)
	}
	if all, _ := store.ListFindings(ctx, b.Name, "", 0); len(all) != 0 {
		t.Fatalf("flap must leave no findings, got %+v", all)
	}

	// drift ×2 → open; clean → resolved (kept); re-drift ×2 → a NEW open row.
	for i := 0; i < 2; i++ {
		if _, err := store.RecordBaselineObservations(ctx, b, mustRun(t, store), drift); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	out, err = store.RecordBaselineObservations(ctx, b, mustRun(t, store), clean)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if out.Resolved != 1 {
		t.Fatalf("want resolved, got %+v", out)
	}
	resolved, _ := store.ListFindings(ctx, b.Name, types.FindingResolved, 0)
	if len(resolved) != 1 || resolved[0].ResolvedAt == nil {
		t.Fatalf("resolved finding must be kept with resolvedAt, got %+v", resolved)
	}
	for i := 0; i < 2; i++ {
		if _, err := store.RecordBaselineObservations(ctx, b, mustRun(t, store), drift); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	open, _ := store.ListFindings(ctx, b.Name, types.FindingOpen, 0)
	if len(open) != 1 || open[0].ID == resolved[0].ID {
		t.Fatalf("re-drift must open a fresh row, got %+v", open)
	}
	if all, _ := store.ListFindings(ctx, b.Name, "", 0); len(all) != 2 {
		t.Fatalf("want resolved history + new open, got %+v", all)
	}
}

func TestFindingAbsentTargetNoTransition(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	b := testBaseline(1)
	if _, err := store.RecordBaselineObservations(ctx, b, mustRun(t, store),
		map[string]BaselineObservation{"vm-1": {Drifted: true}}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// vm-1 failed its next check → no observation at all → no transition.
	out, err := store.RecordBaselineObservations(ctx, b, mustRun(t, store), map[string]BaselineObservation{})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if out != (ObservationOutcome{}) {
		t.Fatalf("absent target must transition nothing, got %+v", out)
	}
	if f := liveFinding(t, store, b.Name, "vm-1"); f == nil || f.Status != types.FindingOpen {
		t.Fatalf("finding must stay open, got %+v", f)
	}
}

func TestBaselineStoreCRUD(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	b := testBaseline(2)
	if err := store.UpsertBaseline(ctx, b); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := store.GetBaseline(ctx, b.Name)
	if err != nil || got.ViewName != "dev-vms" || got.DampingObservations != 2 {
		t.Fatalf("get: %v %+v", err, got)
	}
	all, err := store.ListBaselines(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %v %+v", err, all)
	}
	if err := store.DeleteBaseline(ctx, b.Name); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.DeleteBaseline(ctx, b.Name); err == nil {
		t.Fatalf("second delete must be not-found")
	}
}
