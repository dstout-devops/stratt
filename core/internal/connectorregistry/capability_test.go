package connectorregistry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/homegate"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/types"
)

// fakeManifest is a ManifestFetcher backed by a static addr→advertised-capabilities map,
// so provider verification (ADR-0104 D1) exercises without a live plugin.
func fakeManifest(caps map[string][]string) ManifestFetcher {
	return func(_ context.Context, addr string) ([]string, error) { return caps[addr], nil }
}

// verificationRow fetches one provider's persisted verification outcome (test helper).
func verificationRow(t *testing.T, s *graph.Store, kind, name string) (graph.ProviderVerification, bool) {
	t.Helper()
	rows, err := s.ListProviderVerifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Kind == kind && r.Name == name {
			return r, true
		}
	}
	return graph.ProviderVerification{}, false
}

// TestClassifyRequires is the pure resolution table (ADR-0104 D3): 0 → unmet, 1 → bound, ≥2 →
// ambiguous, and 0-verified-but-declared → a descent pointer (§1.8). No DB.
func TestClassifyRequires(t *testing.T) {
	res := resolution{
		verified:   providerIndex{"keycustodian": 1, "statestore": 2},
		unverified: providerIndex{"certissuer": 1}, // declared but rejected/pending
	}

	if ok, _ := classifyRequires(nil, res); !ok {
		t.Fatal("a declaration that requires nothing must be satisfied")
	}
	if ok, r := classifyRequires([]string{"keycustodian"}, res); !ok {
		t.Fatalf("exactly one verified provider must bind: ok=%v reason=%q", ok, r)
	}
	if ok, r := classifyRequires([]string{"provisioning"}, res); ok || !strings.Contains(r, "no provider") {
		t.Fatalf("zero declared providers must be unmet+observable: ok=%v reason=%q", ok, r)
	}
	if ok, r := classifyRequires([]string{"statestore"}, res); ok || !strings.Contains(r, "ambiguous") {
		t.Fatalf("two verified providers must fail closed as ambiguous (never a silent tiebreak, §2.4): ok=%v reason=%q", ok, r)
	}
	// A declared-but-unverified provider must NOT satisfy, and the reason must point to it (§1.8).
	if ok, r := classifyRequires([]string{"certissuer"}, res); ok || !strings.Contains(r, "declared but failed/pending") {
		t.Fatalf("a declared-but-rejected provider must fail with a descent pointer: ok=%v reason=%q", ok, r)
	}
	// First failing requirement wins the reason (met one doesn't mask the unmet one).
	if ok, r := classifyRequires([]string{"keycustodian", "provisioning"}, res); ok || !strings.Contains(r, "provisioning") {
		t.Fatalf("an unmet requirement alongside a met one must still fail: ok=%v reason=%q", ok, r)
	}
}

// TestActuatorDependencyGate proves the store-backed gate + level-triggered convergence (ADR-0104
// D3/D4) AND the D3 replica-consistency fix: the every-replica Actuator loop resolves against a
// LEADER-ONLY Connector provider purely via the store (verification projection), never local dial
// state, so a follower enables the Actuator the instant its provider is DECLARED and VERIFIED.
func TestActuatorDependencyGate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())
	// The provider at :9091 genuinely advertises statestore in its Manifest.
	r.manifest = fakeManifest(map[string][]string{"localhost:9091": {"statestore"}})

	// A consumer Actuator requiring statestore, with NO provider declared yet.
	if err := s.UpsertActuator(ctx, types.Actuator{Name: "t-consumer", Address: "localhost:9090", PluginIdentity: "p", Requires: []string{"statestore"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-consumer")

	r.ReconcileProviderVerification(ctx)
	r.ReconcileActuators(ctx)
	if _, ok := plugins.Actuator("t-consumer"); ok {
		t.Fatal("a consumer with an unmet requirement must NOT be in the dispatch table (fail closed)")
	}
	st, ok := r.Status("actuator", "t-consumer")
	if !ok || st.Enabled || !strings.Contains(st.Error, "no provider") {
		t.Fatalf("an unmet requirement must surface a PENDING D6 reason (§1.8): %+v ok=%v", st, ok)
	}

	// Declare the provider — a Connector (cross-kind, leader-only). Verification confirms its
	// Manifest advertises statestore; only then does the every-replica Actuator loop count it.
	if err := s.UpsertConnector(ctx, types.Connector{Name: "t-s3", Class: types.ConnectorSyncer, Address: "localhost:9091", PluginIdentity: "s3", Source: types.Source{Kind: "s3", Name: "t-s3"}, Provides: []string{"statestore"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteConnector(ctx, "t-s3")
	defer s.DeleteProviderVerification(ctx, "connector", "t-s3")

	// Before verification runs, the declared provider must NOT yet count (fail closed).
	r.ReconcileActuators(ctx)
	if _, ok := plugins.Actuator("t-consumer"); ok {
		t.Fatal("a declared-but-unverified provider must not satisfy a consumer (fail closed until verified)")
	}

	r.ReconcileProviderVerification(ctx)
	if v, ok := verificationRow(t, s, "connector", "t-s3"); !ok || !v.Verified {
		t.Fatalf("t-s3 must verify (its manifest advertises statestore): %+v ok=%v", v, ok)
	}
	r.ReconcileActuators(ctx)
	if _, ok := plugins.Actuator("t-consumer"); !ok {
		t.Fatal("the consumer must enable once its provider is declared AND verified (D4 convergence)")
	}
	if st, _ := r.Status("actuator", "t-consumer"); !st.Enabled || st.Error != "" {
		t.Fatalf("the consumer's status must flip to enabled: %+v", st)
	}
}

// TestPhantomProviderRejected is the load-bearing ADR-0104 D1 gate: a provider that declares
// `provides: [statestore]` but whose Manifest does NOT advertise it is a PHANTOM — verification
// marks it verified=false with a queryable reason (§1.8) and it does NOT count toward any
// consumer, which stays pending. The failure surfaces at declaration, never at Run-time (§1.5).
func TestPhantomProviderRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())
	// The phantom at :9092 advertises the WRONG capability (artifactstore, not statestore).
	r.manifest = fakeManifest(map[string][]string{"localhost:9092": {"artifactstore"}})

	if err := s.UpsertConnector(ctx, types.Connector{Name: "t-phantom", Class: types.ConnectorSyncer, Address: "localhost:9092", PluginIdentity: "ph", Source: types.Source{Kind: "ph", Name: "t-phantom"}, Provides: []string{"statestore"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteConnector(ctx, "t-phantom")
	defer s.DeleteProviderVerification(ctx, "connector", "t-phantom")

	if err := s.UpsertActuator(ctx, types.Actuator{Name: "t-phantom-consumer", Address: "localhost:9090", PluginIdentity: "c", Requires: []string{"statestore"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-phantom-consumer")

	r.ReconcileProviderVerification(ctx)
	v, ok := verificationRow(t, s, "connector", "t-phantom")
	if !ok || v.Verified || !strings.Contains(v.Reason, "phantom") {
		t.Fatalf("a phantom provider must be recorded verified=false with a phantom reason (§1.8): %+v ok=%v", v, ok)
	}

	r.ReconcileActuators(ctx)
	if _, ok := plugins.Actuator("t-phantom-consumer"); ok {
		t.Fatal("a phantom provider must NOT satisfy a consumer's gate (§1.5 — no Run-time surprise)")
	}
	st, _ := r.Status("actuator", "t-phantom-consumer")
	// The phantom does not satisfy — and the consumer's reason POINTS at the rejected provider
	// (§1.8 descent), distinguishing "declared but rejected" from "none declared".
	if st.Enabled || !strings.Contains(st.Error, "declared but failed/pending") {
		t.Fatalf("the consumer must stay pending with a descent pointer to the rejected provider: %+v", st)
	}
}

// TestVerificationTransientBlipPreservesVerdict is guardian Finding 1: a provider that verified
// once must NOT be dropped to verified=false by a later TRANSIENT manifest-fetch failure — else a
// blip in the leader's pass would collapse an established provider count and silently tiebreak a
// consumer (precedence-by-liveness, §2.4/D3). Only a STRUCTURAL mismatch may zero a verdict.
func TestVerificationTransientBlipPreservesVerdict(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())

	blip := false
	r.manifest = func(_ context.Context, _ string) ([]string, error) {
		if blip {
			return nil, errors.New("dial blip")
		}
		return []string{"keycustodian"}, nil
	}

	if err := s.UpsertConnector(ctx, types.Connector{Name: "t-blip", Class: types.ConnectorSyncer, Address: "localhost:9099", PluginIdentity: "b", Source: types.Source{Kind: "b", Name: "t-blip"}, Provides: []string{"keycustodian"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteConnector(ctx, "t-blip")
	defer s.DeleteProviderVerification(ctx, "connector", "t-blip")

	r.ReconcileProviderVerification(ctx)
	if v, ok := verificationRow(t, s, "connector", "t-blip"); !ok || !v.Verified {
		t.Fatalf("provider must verify on a successful fetch: %+v ok=%v", v, ok)
	}

	// A transient fetch failure must PRESERVE the confirmed verdict — not drop it to false.
	blip = true
	r.ReconcileProviderVerification(ctx)
	if v, ok := verificationRow(t, s, "connector", "t-blip"); !ok || !v.Verified {
		t.Fatalf("a transient fetch blip must preserve the last-known verified verdict (§2.4/D3, Finding 1): %+v ok=%v", v, ok)
	}
}

// TestActuatorDependencyAmbiguous proves ≥2 VERIFIED providers fails closed as pending — the
// registry never silently tiebreaks which provider (§2.4); an estate binding (follow-up)
// disambiguates.
func TestActuatorDependencyAmbiguous(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())
	r.manifest = fakeManifest(map[string][]string{"localhost:9090": {"keycustodian"}})

	for _, name := range []string{"t-p1", "t-p2"} {
		if err := s.UpsertActuator(ctx, types.Actuator{Name: name, Address: "localhost:9090", PluginIdentity: name, Provides: []string{"keycustodian"}}); err != nil {
			t.Fatal(err)
		}
		defer s.DeleteActuator(ctx, name)
		defer s.DeleteProviderVerification(ctx, "actuator", name)
	}
	if err := s.UpsertActuator(ctx, types.Actuator{Name: "t-amb-consumer", Address: "localhost:9090", PluginIdentity: "c", Requires: []string{"keycustodian"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-amb-consumer")

	r.ReconcileProviderVerification(ctx)
	r.ReconcileActuators(ctx)
	if _, ok := plugins.Actuator("t-amb-consumer"); ok {
		t.Fatal("a consumer of an AMBIGUOUS capability must NOT enable (no silent tiebreak, §2.4)")
	}
	st, _ := r.Status("actuator", "t-amb-consumer")
	if st.Enabled || !strings.Contains(st.Error, "ambiguous") {
		t.Fatalf("≥2 verified providers must surface an 'ambiguous' pending reason: %+v", st)
	}
}
