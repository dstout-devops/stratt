package triggerengine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	neturl "net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/rules"
	"github.com/dstout-devops/stratt/types"
)

// ── ADR-0162 · does this Trigger fire, given the events already seen? ─────────────────────────
//
// Against a REAL store, because the decision IS the store's now: the whole point of D2 is that the
// bookkeeping stopped being a map in this process. A fake store would test the fake.

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
	name := fmt.Sprintf("stratt_trig_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close()
	})
	u, _ := neturl.Parse(url)
	u.Path = "/" + name
	store, err := graph.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect+migrate: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func testEngine(t *testing.T) *Engine {
	t.Helper()
	e := &Engine{Store: testStore(t), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// Run() does this; decide() is being exercised without it.
	e.programs = map[string]*rules.Program{}
	e.specs = map[string]string{}
	return e
}

// fire runs one event through decide and reports whether the Trigger fired, resetting the window
// exactly as handle() does so a sequence of events behaves as it does in production.
func fire(t *testing.T, e *Engine, tr types.Trigger, payload map[string]any) bool {
	t.Helper()
	ev := types.EmitterEvent{Emitter: tr.Emitter, Payload: payload}
	ok, key, err := e.decide(context.Background(), e.Log, tr, ev)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if ok {
		if err := e.Store.TriggerWindowFired(context.Background(), tr.Name, key); err != nil {
			t.Fatalf("window reset: %v", err)
		}
	}
	return ok
}

func flapTrigger(mut func(*types.Trigger)) types.Trigger {
	tr := types.Trigger{
		Name: "flap", Kind: types.TriggerEvent, Emitter: "alerts",
		When: "event.alertname == 'LinkFlap'", WorkflowName: "remediate",
	}
	mut(&tr)
	return tr
}

// THE REGRESSION THAT MATTERS MOST. Every Trigger that exists today declares no pattern, and must
// behave exactly as it did before ADR-0162: fire on every match, never on a non-match. A feature
// that quietly damped the estate's existing Triggers would be a far worse bug than the one it fixes.
func TestAPatternlessTriggerFiresOnEveryMatch(t *testing.T) {
	e := testEngine(t)
	tr := flapTrigger(func(*types.Trigger) {})
	for i := 1; i <= 4; i++ {
		if !fire(t, e, tr, map[string]any{"alertname": "LinkFlap"}) {
			t.Fatalf("match %d did not fire; a Trigger with no declared pattern fires every time", i)
		}
	}
	if fire(t, e, tr, map[string]any{"alertname": "SomethingElse"}) {
		t.Error("a non-matching event fired")
	}
}

// D3: the storm, not the alert. Four flaps are noise; the fifth is an incident.
func TestCountFiresOnTheNthMatchAndNotBefore(t *testing.T) {
	e := testEngine(t)
	tr := flapTrigger(func(tr *types.Trigger) { tr.Count = 5; tr.WithinSeconds = 600 })
	ev := map[string]any{"alertname": "LinkFlap"}

	for i := 1; i <= 4; i++ {
		if fire(t, e, tr, ev) {
			t.Fatalf("fired on match %d of 5 — the threshold is not being counted", i)
		}
	}
	if !fire(t, e, tr, ev) {
		t.Fatal("the fifth match must fire")
	}

	// D3's reset, and the reason it is a reset rather than a slide: the 6th, 7th and 8th flaps of a
	// storm must NOT each produce a Run. That is the problem being solved, not a side effect.
	for i := 6; i <= 9; i++ {
		if fire(t, e, tr, ev) {
			t.Fatalf("event %d fired — a sliding window turns one storm into a storm of Runs", i)
		}
	}
	if !fire(t, e, tr, ev) {
		t.Fatal("the tenth event completes the second window and must fire")
	}
}

// A non-matching event must not advance the count: "five link-flaps" means five link-flaps, not five
// events that happened to arrive on the same Emitter.
func TestOnlyMatchingEventsAdvanceTheCount(t *testing.T) {
	e := testEngine(t)
	tr := flapTrigger(func(tr *types.Trigger) { tr.Count = 3; tr.WithinSeconds = 600 })
	for i := 0; i < 5; i++ {
		if fire(t, e, tr, map[string]any{"alertname": "Unrelated"}) {
			t.Fatal("a non-matching event fired")
		}
	}
	for i := 1; i <= 2; i++ {
		if fire(t, e, tr, map[string]any{"alertname": "LinkFlap"}) {
			t.Fatalf("matching event %d fired early — non-matching events polluted the count", i)
		}
	}
	if !fire(t, e, tr, map[string]any{"alertname": "LinkFlap"}) {
		t.Fatal("the third MATCHING event must fire")
	}
}

// D4: correlation keeps two services apart. Three flaps on checkout plus two on billing is not five
// flaps anywhere — and firing as though it were would converge an estate nothing is wrong with.
func TestCorrelatedCountsDoNotPoolAcrossKeys(t *testing.T) {
	e := testEngine(t)
	tr := flapTrigger(func(tr *types.Trigger) {
		tr.Count = 3
		tr.WithinSeconds = 600
		tr.CorrelateBy = "event.service"
	})
	flap := func(svc string) bool {
		return fire(t, e, tr, map[string]any{"alertname": "LinkFlap", "service": svc})
	}
	if flap("checkout") || flap("billing") || flap("checkout") || flap("billing") {
		t.Fatal("fired before any single service reached three")
	}
	if !flap("checkout") {
		t.Fatal("checkout's third flap must fire")
	}
	// Billing has now also seen three, and it fires on its own count — which is the proof that
	// checkout's reset was scoped to checkout's window rather than clearing the Trigger's.
	if !flap("billing") {
		t.Error("billing's third flap did not fire — checkout's reset cleared billing's window too")
	}
}

// D4: `allOf` waits for every condition, satisfied by events sharing one key.
func TestAllOfWaitsForEveryConditionUnderOneKey(t *testing.T) {
	e := testEngine(t)
	tr := types.Trigger{
		Name: "deploy-then-fail", Kind: types.TriggerEvent, Emitter: "alerts",
		AllOf: []string{
			"event.kind == 'deploy.finished'",
			"event.kind == 'healthcheck.failed'",
		},
		CorrelateBy: "event.service", WithinSeconds: 900, WorkflowName: "rollback",
	}
	post := func(kind, svc string) bool {
		return fire(t, e, tr, map[string]any{"kind": kind, "service": svc})
	}

	if post("deploy.finished", "checkout") {
		t.Fatal("one condition of two fired the Trigger")
	}
	// THE HAZARD: the other condition satisfied by a DIFFERENT service must not complete the pattern.
	if post("healthcheck.failed", "billing") {
		t.Fatal("a deploy on checkout and a failure on billing fired — this is exactly the " +
			"'somewhere and somewhere' mistake correlateBy exists to make unavailable")
	}
	if !post("healthcheck.failed", "checkout") {
		t.Fatal("both conditions satisfied under one key must fire")
	}
}

// The same condition satisfied repeatedly is still one condition — two deploys are not a deploy and
// a failed health check.
func TestAllOfIsNotSatisfiedByRepeatingOneCondition(t *testing.T) {
	e := testEngine(t)
	tr := types.Trigger{
		Name: "repeat", Kind: types.TriggerEvent, Emitter: "alerts",
		AllOf:       []string{"event.kind == 'a'", "event.kind == 'b'"},
		CorrelateBy: "event.service", WithinSeconds: 900, WorkflowName: "w",
	}
	for i := 0; i < 5; i++ {
		if fire(t, e, tr, map[string]any{"kind": "a", "service": "checkout"}) {
			t.Fatal("one condition, repeated, completed a two-condition pattern")
		}
	}
}

// An event carrying no value for the declared key participates in NO window. The alternative —
// pooling them under "" — recreates the cross-service hazard among exactly the events we know least
// about.
func TestAnUncorrelatedEventJoinsNoWindow(t *testing.T) {
	e := testEngine(t)
	tr := types.Trigger{
		Name: "uncorrelated", Kind: types.TriggerEvent, Emitter: "alerts",
		AllOf:       []string{"event.kind == 'a'", "event.kind == 'b'"},
		CorrelateBy: "event.service", WithinSeconds: 900, WorkflowName: "w",
	}
	if fire(t, e, tr, map[string]any{"kind": "a"}) || fire(t, e, tr, map[string]any{"kind": "b"}) {
		t.Fatal("events with no correlation value fired")
	}
	// …and they left nothing behind: a real correlated pair still needs BOTH of its own events.
	if fire(t, e, tr, map[string]any{"kind": "a", "service": "checkout"}) {
		t.Fatal("an uncorrelated event pre-satisfied a condition for a real key")
	}
}

// THE DEFECT ADR-0162 CLAIMS TO FIX. Cooldown bookkeeping used to live in a map in one process: it
// reset on restart and each replica kept its own. A NEW Engine over the same store is both a
// restarted pod and a second replica.
func TestCooldownSurvivesARestart(t *testing.T) {
	first := testEngine(t)
	tr := flapTrigger(func(tr *types.Trigger) { tr.CooldownSeconds = 300 })
	ev := map[string]any{"alertname": "LinkFlap"}

	if !fire(t, first, tr, ev) {
		t.Fatal("the first match must fire")
	}
	if fire(t, first, tr, ev) {
		t.Fatal("the second match must be suppressed by the cooldown")
	}

	restarted := &Engine{Store: first.Store, Log: first.Log,
		programs: map[string]*rules.Program{}, specs: map[string]string{}}
	if fire(t, restarted, tr, ev) {
		t.Fatal("the cooldown did not survive a restart — this is the bug D2 exists to fix, and " +
			"it is invisible in production: the storm damping an estate declares is not the one it gets")
	}
}
