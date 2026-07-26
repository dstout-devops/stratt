package connectorregistry

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/homegate"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	"github.com/dstout-devops/stratt/types"
)

func testStore(t *testing.T) *graph.Store {
	t.Helper()
	dsn := os.Getenv("STRATT_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://stratt:stratt-dev@localhost:5432/stratt?sslmode=disable"
	}
	s, err := graph.Connect(context.Background(), dsn)
	if err != nil {
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}
	return s
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// grpc.NewClient is lazy — a valid target never connects until an RPC, so the actuator
// register/dispatch-map path exercises without a live plugin.
func lazyDial(addr string) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// TestActuatorReconcile proves the every-replica Actuator path (ADR-0103): a declared
// Actuator + its Actions land in the dispatch table on reconcile; removing the declaration
// tears them down; the runtime status tracks it (D6).
func TestActuatorReconcile(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())
	// A declared actionName is now checked against the plugin's own advertisement, so a
	// dispatch-table test needs a plugin that advertises it (there is no live one here).
	r.manifest = fakeActions(map[string][]AdvertisedAction{
		"localhost:9090": {{Name: "t-helm/deploy", InputContract: "actions/helm/deploy.input"}},
	})

	if err := s.UpsertActuator(ctx, types.Actuator{Name: "t-helm", Address: "localhost:9090", PluginIdentity: "helm", DryRunnable: true, ActionNames: []string{"t-helm/deploy"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-helm")

	r.ReconcileActuators(ctx)
	if _, ok := plugins.Actuator("t-helm"); !ok {
		t.Fatal("a declared actuator must be registered in the dispatch table")
	}
	if _, ok := plugins.Action("t-helm/deploy"); !ok {
		t.Fatal("the actuator's declared Action must be registered")
	}
	if st, ok := r.Status("actuator", "t-helm"); !ok || !st.Enabled {
		t.Fatalf("runtime status must be enabled (D6): %+v ok=%v", st, ok)
	}
	// A second reconcile with the same spec is a no-op (no re-register churn).
	r.ReconcileActuators(ctx)
	if _, ok := plugins.Actuator("t-helm"); !ok {
		t.Fatal("an unchanged actuator must stay registered")
	}

	// Remove the declaration → the next reconcile deregisters it (no restart).
	if err := s.DeleteActuator(ctx, "t-helm"); err != nil {
		t.Fatal(err)
	}
	r.ReconcileActuators(ctx)
	if _, ok := plugins.Actuator("t-helm"); ok {
		t.Fatal("an undeclared actuator must be deregistered")
	}
	if _, ok := plugins.Action("t-helm/deploy"); ok {
		t.Fatal("the deregistered actuator's Action must be gone too")
	}
	if _, ok := r.Status("actuator", "t-helm"); ok {
		t.Fatal("status must be cleared on disable")
	}
}

// TestActuatorCollisionRejectNotCrash proves D4/D6: a §2.4 name collision is rejected +
// surfaced as a status error — the daemon does NOT crash.
func TestActuatorCollisionRejectNotCrash(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// The in-tree predicate claims "t-collide" — a declared actuator of that name collides.
	plugins := orchestrate.NewPluginRegistry(func(n string) bool { return n == "t-collide" }, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())

	if err := s.UpsertActuator(ctx, types.Actuator{Name: "t-collide", Address: "localhost:9090", PluginIdentity: "p"}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-collide")

	r.ReconcileActuators(ctx) // must not panic/crash
	if _, ok := plugins.Actuator("t-collide"); ok {
		t.Fatal("a colliding actuator must be rejected (§2.4)")
	}
	st, ok := r.Status("actuator", "t-collide")
	if !ok || st.Enabled || st.Error == "" {
		t.Fatalf("a collision must surface a status error (D6), got %+v ok=%v", st, ok)
	}
}

// TestReconcileRace runs the reconcile loop concurrently with dispatch reads + status reads
// under -race — the S5 lock discipline (entry mu + status smu + the shared PluginRegistry).
func TestReconcileRace(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())
	if err := s.UpsertActuator(ctx, types.Actuator{Name: "t-race", Address: "localhost:9090", PluginIdentity: "p", ActionNames: []string{"t-race/x"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-race")

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			r.ReconcileActuators(ctx)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			_, _ = plugins.Actuator("t-race")
			_, _ = plugins.Action("t-race/x")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			_ = r.Statuses()
		}
	}()
	wg.Wait()
}

// TestActuatorGrantCarriesDeclaredBounds pins ADR-0117 D3a's grant half: the CaC
// declaration's facetNamespaces/identitySchemes must reach the govern grant, or a
// declared EE-Job Actuator is not equivalent to a boot-registered one — it would run
// and then have every fact write-back refused by the hub, which is exactly the kind of
// silently-weaker seam §1.8 forbids.
func TestActuatorGrantCarriesDeclaredBounds(t *testing.T) {
	g := actuatorGrant(types.Actuator{
		Name: "ansible-crypto", PluginIdentity: "ansible", Tier: "trusted",
		FacetNamespaces: []string{"app.config", "os.kernel"},
		IdentitySchemes: []string{"host.name"},
	})
	if g.PluginIdentity != "ansible" || g.Tier != pluginhost.TierTrusted {
		t.Fatalf("identity/tier must survive: %+v", g)
	}
	if len(g.FacetNamespaces) != 2 {
		t.Fatalf("declared facet bounds must reach the grant, got %v", g.FacetNamespaces)
	}
	if len(g.IdentitySchemes) != 1 || g.IdentitySchemes[0] != "host.name" {
		t.Fatalf("declared identity schemes must reach the grant, got %v", g.IdentitySchemes)
	}
	// The bound stays BOUNDED — declaring nothing grants nothing, never a wildcard (MF3).
	bare := actuatorGrant(types.Actuator{Name: "helm", PluginIdentity: "helm", Tier: "trusted"})
	if len(bare.FacetNamespaces) != 0 || len(bare.IdentitySchemes) != 0 {
		t.Fatalf("an Actuator declaring no grant must get NO facet authority: %+v", bare)
	}
}
