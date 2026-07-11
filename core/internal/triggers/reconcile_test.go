package triggers

import (
	"context"
	"fmt"
	"log/slog"
	neturl "net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// testStore mirrors the graph/desiredstate integration-test helper:
// throwaway database, migrations applied, skip when no substrate reachable.
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

func testTemporal(t *testing.T) client.Client {
	t.Helper()
	addr := os.Getenv("STRATT_TEST_TEMPORAL_ADDRESS")
	if addr == "" {
		addr = "localhost:7233"
	}
	c, err := client.Dial(client.Options{HostPort: addr})
	if err != nil {
		t.Skipf("no temporal reachable (%v) — run `task dev:up`", err)
	}
	t.Cleanup(c.Close)
	return c
}

// TestReconcileLifecycle drives a declaration through create → noop →
// update (cron + paused) → out-of-band prune → delete against a live
// Temporal, asserting the Schedule projection converges each time.
func TestReconcileLifecycle(t *testing.T) {
	tc := testTemporal(t)
	store := testStore(t)
	ctx := context.Background()
	r := &Reconciler{Temporal: tc, Store: store, Log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	sc := tc.ScheduleClient()

	name := fmt.Sprintf("itest-%d", time.Now().UnixNano())
	t.Cleanup(func() { // belt-and-braces: never leave test schedules behind
		_ = sc.GetHandle(context.Background(), ScheduleID(name)).Delete(context.Background())
	})

	trig := types.Trigger{
		Name: name, Kind: types.TriggerSchedule, Cron: "0 2 * * *",
		ViewName: "all-vms", Principal: "svc-1", CredentialRefs: []string{"vc"},
	}
	if err := store.UpsertTrigger(ctx, trig); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	handle := sc.GetHandle(ctx, ScheduleID(name))
	desc, err := handle.Describe(ctx)
	if err != nil {
		t.Fatalf("schedule should exist after reconcile: %v", err)
	}
	if schedulePaused(desc) {
		t.Fatal("schedule should start unpaused")
	}
	wantHash, err := declarationHash(trig)
	if err != nil {
		t.Fatal(err)
	}
	if got := actionHash(desc); got != wantHash {
		t.Fatalf("action memo hash mismatch: got %q want %q", got, wantHash)
	}

	// Convergence is a no-op when nothing changed.
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	desc2, err := handle.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !desc2.Info.LastUpdateAt.Equal(desc.Info.LastUpdateAt) {
		t.Fatal("unchanged declaration must not update the schedule")
	}

	// Cron + paused change converges via update.
	trig.Cron = "0 3 * * *"
	trig.Paused = true
	if err := store.UpsertTrigger(ctx, trig); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	desc3, err := handle.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !schedulePaused(desc3) {
		t.Fatal("declared paused must converge")
	}
	wantHash2, _ := declarationHash(trig)
	if got := actionHash(desc3); got != wantHash2 {
		t.Fatalf("updated hash mismatch: got %q want %q", got, wantHash2)
	}

	// An out-of-band schedule under our prefix is removed (§1.2: the
	// Schedule set is a projection of the declarations, adds and revokes).
	rogue := fmt.Sprintf("rogue-%d", time.Now().UnixNano())
	_, err = sc.Create(ctx, client.ScheduleOptions{
		ID:   ScheduleID(rogue),
		Spec: client.ScheduleSpec{CronExpressions: []string{"0 4 * * *"}},
		Action: &client.ScheduleWorkflowAction{
			Workflow: "RunAgainstView", TaskQueue: "stratt-runs",
		},
		Paused: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Schedule List is eventually consistent (visibility store), so the
	// rogue may not be visible to the first pass — poll a few cycles, the
	// way the reconciler's cadence does in production.
	pruned := false
	for range 20 {
		if err := r.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := sc.GetHandle(ctx, ScheduleID(rogue)).Describe(ctx); err != nil {
			pruned = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !pruned {
		_ = sc.GetHandle(ctx, ScheduleID(rogue)).Delete(ctx)
		t.Fatal("out-of-band schedule under the stratt prefix must be pruned")
	}

	// Deleting the declaration deletes the schedule.
	if err := store.DeleteTrigger(ctx, name); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Describe(ctx); err == nil {
		t.Fatal("undeclared trigger's schedule must be deleted")
	}
}
