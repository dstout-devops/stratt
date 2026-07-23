package connectorregistry

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/core/internal/homegate"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/types"
)

// TestClassifyRequires is the pure resolution table (ADR-0104 D3): 0 → unmet, 1 → bound, ≥2 →
// ambiguous. No DB — the decision logic in isolation.
func TestClassifyRequires(t *testing.T) {
	idx := providerIndex{"keycustodian": 1, "statestore": 2}

	if ok, _ := classifyRequires(nil, idx); !ok {
		t.Fatal("a declaration that requires nothing must be satisfied")
	}
	if ok, r := classifyRequires([]string{"keycustodian"}, idx); !ok {
		t.Fatalf("exactly one provider must bind: ok=%v reason=%q", ok, r)
	}
	if ok, r := classifyRequires([]string{"certissuer"}, idx); ok || !strings.Contains(r, "no provider") {
		t.Fatalf("zero providers must be unmet+observable: ok=%v reason=%q", ok, r)
	}
	if ok, r := classifyRequires([]string{"statestore"}, idx); ok || !strings.Contains(r, "ambiguous") {
		t.Fatalf("two providers must fail closed as ambiguous (never a silent tiebreak, §2.4): ok=%v reason=%q", ok, r)
	}
	// First failing requirement wins the reason (met one doesn't mask the unmet one).
	if ok, r := classifyRequires([]string{"keycustodian", "certissuer"}, idx); ok || !strings.Contains(r, "certissuer") {
		t.Fatalf("an unmet requirement alongside a met one must still fail: ok=%v reason=%q", ok, r)
	}
}

// TestActuatorDependencyGate proves the store-backed gate + level-triggered convergence (ADR-0104
// D3/D4) AND the D3 replica-consistency fix: the every-replica Actuator loop resolves against a
// LEADER-ONLY Connector provider purely via the store (never local dial state), so a follower
// enables the Actuator the instant its provider is DECLARED.
func TestActuatorDependencyGate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())

	// A consumer Actuator requiring statestore, with NO provider declared yet.
	if err := s.UpsertActuator(ctx, types.Actuator{Name: "t-consumer", Address: "localhost:9090", PluginIdentity: "p", Requires: []string{"statestore"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-consumer")

	r.ReconcileActuators(ctx)
	if _, ok := plugins.Actuator("t-consumer"); ok {
		t.Fatal("a consumer with an unmet requirement must NOT be in the dispatch table (fail closed)")
	}
	st, ok := r.Status("actuator", "t-consumer")
	if !ok || st.Enabled || !strings.Contains(st.Error, "no provider") {
		t.Fatalf("an unmet requirement must surface a PENDING D6 reason (§1.8): %+v ok=%v", st, ok)
	}

	// Declare a provider — as a Connector (cross-kind, leader-only). The Actuator loop must resolve
	// it via the store index without the Connector ever being locally dialed (the D3 hazard fix).
	if err := s.UpsertConnector(ctx, types.Connector{Name: "t-s3", Class: types.ConnectorSyncer, Address: "localhost:9091", PluginIdentity: "s3", Source: types.Source{Kind: "s3", Name: "t-s3"}, Provides: []string{"statestore"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteConnector(ctx, "t-s3")

	r.ReconcileActuators(ctx)
	if _, ok := plugins.Actuator("t-consumer"); !ok {
		t.Fatal("the consumer must enable once its provider is declared (level-triggered convergence, D4)")
	}
	if st, _ := r.Status("actuator", "t-consumer"); !st.Enabled || st.Error != "" {
		t.Fatalf("the consumer's status must flip to enabled: %+v", st)
	}
}

// TestActuatorDependencyAmbiguous proves ≥2 providers fails closed as pending — the registry never
// silently tiebreaks which provider (§2.4); an estate binding (ADR-0104 follow-up) disambiguates.
func TestActuatorDependencyAmbiguous(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())

	for _, name := range []string{"t-p1", "t-p2"} {
		if err := s.UpsertActuator(ctx, types.Actuator{Name: name, Address: "localhost:9090", PluginIdentity: name, Provides: []string{"keycustodian"}}); err != nil {
			t.Fatal(err)
		}
		defer s.DeleteActuator(ctx, name)
	}
	if err := s.UpsertActuator(ctx, types.Actuator{Name: "t-amb-consumer", Address: "localhost:9090", PluginIdentity: "c", Requires: []string{"keycustodian"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-amb-consumer")

	r.ReconcileActuators(ctx)
	if _, ok := plugins.Actuator("t-amb-consumer"); ok {
		t.Fatal("a consumer of an AMBIGUOUS capability must NOT enable (no silent tiebreak, §2.4)")
	}
	st, _ := r.Status("actuator", "t-amb-consumer")
	if st.Enabled || !strings.Contains(st.Error, "ambiguous") {
		t.Fatalf("≥2 providers must surface an 'ambiguous' pending reason: %+v", st)
	}
}
