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
	return func(_ context.Context, addr string) (PluginManifest, error) {
		return PluginManifest{Capabilities: caps[addr]}, nil
	}
}

// fakeProvider is fakeManifest plus the class→Action implementations a provider advertises
// (ADR-0140 D1) — what resolution now READS instead of deriving.
func fakeProvider(m map[string]PluginManifest) ManifestFetcher {
	return func(_ context.Context, addr string) (PluginManifest, error) { return m[addr], nil }
}

// implementing builds a Manifest advertising one class and the Action that IS it.
func implementing(class, action string) PluginManifest {
	return PluginManifest{
		Capabilities: []string{class},
		Actions:      []AdvertisedAction{{Name: action, InputContract: "capabilities/" + class + ".input", Implements: class}},
	}
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

// TestResolveCapabilityAction proves the ADR-0105 dispatch-time resolution: a required capability
// maps to the single VERIFIED provider's ADVERTISED implementation (ADR-0140 D1), and fails closed
// on none/ambiguous/unadvertised.
func TestResolveCapabilityAction(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())
	r.manifest = fakeProvider(map[string]PluginManifest{
		"localhost:9090": implementing("statestore", "awss3/statestore-resolve"),
	})

	// No provider declared yet → resolution fails closed.
	if _, err := r.ResolveCapabilityAction(ctx, "statestore"); err == nil {
		t.Fatal("no provider must fail closed")
	}

	// A verified provider that declares its resolve Action → resolves to it.
	if err := s.UpsertActuator(ctx, types.Actuator{Name: "t-s3-state", Address: "localhost:9090", PluginIdentity: "awss3", ActionNames: []string{"awss3/statestore-resolve"}, Provides: []string{"statestore"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-s3-state")
	defer s.DeleteProviderVerification(ctx, "actuator", "t-s3-state")

	// Before verification, the declared-but-unverified provider must NOT resolve (fail closed).
	if _, err := r.ResolveCapabilityAction(ctx, "statestore"); err == nil {
		t.Fatal("a declared-but-unverified provider must not resolve")
	}
	r.ReconcileProviderVerification(ctx)
	action, err := r.ResolveCapabilityAction(ctx, "statestore")
	if err != nil {
		t.Fatalf("a verified provider must resolve: %v", err)
	}
	if action != "awss3/statestore-resolve" {
		t.Fatalf("resolution must return the ADVERTISED implementation, got %q", action)
	}

	// A verified provider advertising the capability but NO implementation of it → the third
	// failure (ADR-0140 D5). It used to surface as "does not declare its resolve Action
	// <computed name>", which pointed the reader at a name core invented.
	if err := s.UpsertActuator(ctx, types.Actuator{Name: "t-bad-state", Address: "localhost:9091", PluginIdentity: "bad", Provides: []string{"artifactstore"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-bad-state")
	defer s.DeleteProviderVerification(ctx, "actuator", "t-bad-state")
	r.manifest = fakeProvider(map[string]PluginManifest{
		"localhost:9090": implementing("statestore", "awss3/statestore-resolve"),
		"localhost:9091": {Capabilities: []string{"artifactstore"}}, // class advertised, no implementation
	})
	r.ReconcileProviderVerification(ctx)
	_, err = r.ResolveCapabilityAction(ctx, "artifactstore")
	if err == nil {
		t.Fatal("a provider advertising no implementation must fail closed")
	}
	if !strings.Contains(err.Error(), "advertises no Action implementing the class") {
		t.Fatalf("the diagnostic must point at the plugin, not at a name core invented: %v", err)
	}

	// Two verified statestore providers → ambiguous (needs an estate binding, §2.4).
	if err := s.UpsertActuator(ctx, types.Actuator{Name: "t-s3-state2", Address: "localhost:9090", PluginIdentity: "awss3", ActionNames: []string{"awss3/statestore-resolve"}, Provides: []string{"statestore"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-s3-state2")
	defer s.DeleteProviderVerification(ctx, "actuator", "t-s3-state2")
	r.ReconcileProviderVerification(ctx)
	if _, err := r.ResolveCapabilityAction(ctx, "statestore"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("≥2 providers must be ambiguous (no silent tiebreak, §2.4): %v", err)
	}
}

// TestResolveCapabilityActionHonorsANonConventionalName is the point of ADR-0140 D1, and the one
// case the old mechanism could not express. Core used to compute `<pluginIdentity>/<class>-resolve`
// and demand the provider have declared exactly that, so a NetBox plugin whose Action is called
// `netbox/allocate-prefix` could not provide `ipam` whatever its Manifest said — the class exists to
// make the provider swappable, and deriving the name constrained the provider's internals instead.
// The name below is one the convention could never have produced.
func TestResolveCapabilityActionHonorsANonConventionalName(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())
	r.manifest = fakeProvider(map[string]PluginManifest{
		"localhost:9093": implementing("ipam", "netbox/allocate-prefix"),
	})

	if err := s.UpsertActuator(ctx, types.Actuator{Name: "t-nb-ipam", Address: "localhost:9093", PluginIdentity: "netbox", ActionNames: []string{"netbox/allocate-prefix"}, Provides: []string{"ipam"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-nb-ipam")
	defer s.DeleteProviderVerification(ctx, "actuator", "t-nb-ipam")

	r.ReconcileProviderVerification(ctx)
	action, err := r.ResolveCapabilityAction(ctx, "ipam")
	if err != nil {
		t.Fatalf("a provider naming its own implementation must resolve — core does not own the plugin's namespace: %v", err)
	}
	if action != "netbox/allocate-prefix" {
		t.Fatalf("resolution must carry the advertised token opaquely, got %q", action)
	}
}

// A class the operator did NOT grant cannot be self-admitted by advertising an implementation of
// it: the Manifest advertises, the grant is truth (§1.5). Without this a plugin could route a
// consumer's capability requirement to itself by claiming a class nobody granted it.
func TestAdvertisedImplementationForAnUngrantedClassIsIgnored(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())
	// The plugin claims BOTH classes; the estate grants only statestore.
	r.manifest = fakeProvider(map[string]PluginManifest{
		"localhost:9094": {
			Capabilities: []string{"statestore", "keycustodian"},
			Actions: []AdvertisedAction{
				{Name: "awss3/statestore-resolve", InputContract: "capabilities/statestore.input", Implements: "statestore"},
				{Name: "awss3/keys-resolve", InputContract: "capabilities/statestore.input", Implements: "keycustodian"},
			},
		},
	})

	if err := s.UpsertActuator(ctx, types.Actuator{Name: "t-s3-grant", Address: "localhost:9094", PluginIdentity: "awss3", ActionNames: []string{"awss3/statestore-resolve", "awss3/keys-resolve"}, Provides: []string{"statestore"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-s3-grant")
	defer s.DeleteProviderVerification(ctx, "actuator", "t-s3-grant")

	r.ReconcileProviderVerification(ctx)
	row, ok := verificationRow(t, s, "actuator", "t-s3-grant")
	if !ok || !row.Verified {
		t.Fatalf("the provider is verified for what it WAS granted: %+v ok=%v", row, ok)
	}
	if got := row.Implements["statestore"]; got != "awss3/statestore-resolve" {
		t.Fatalf("the granted class's implementation must be recorded, got %q", got)
	}
	if got, present := row.Implements["keycustodian"]; present {
		t.Fatalf("an implementation for an ungranted class must not be recorded, got %q", got)
	}
}

// Two Actions claiming to BE the same class is unresolvable without a tiebreak, and picking one
// is the implicit precedence §2.4 exists to refuse.
func TestTwoImplementationsOfOneClassIsAPhantom(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())
	r.manifest = fakeProvider(map[string]PluginManifest{
		"localhost:9095": {
			Capabilities: []string{"ipam"},
			Actions: []AdvertisedAction{
				{Name: "netbox/ipam-resolve", InputContract: "capabilities/ipam.input", Implements: "ipam"},
				{Name: "netbox/allocate-prefix", InputContract: "capabilities/ipam.input", Implements: "ipam"},
			},
		},
	})

	if err := s.UpsertActuator(ctx, types.Actuator{Name: "t-nb-dup", Address: "localhost:9095", PluginIdentity: "netbox", ActionNames: []string{"netbox/ipam-resolve"}, Provides: []string{"ipam"}}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-nb-dup")
	defer s.DeleteProviderVerification(ctx, "actuator", "t-nb-dup")

	r.ReconcileProviderVerification(ctx)
	row, ok := verificationRow(t, s, "actuator", "t-nb-dup")
	if !ok || row.Verified {
		t.Fatalf("a provider advertising two implementations of one class must not verify: %+v ok=%v", row, ok)
	}
	if !strings.Contains(row.Reason, "two implementations") {
		t.Fatalf("the reason must name the ambiguity (§1.8): %q", row.Reason)
	}
	if _, err := r.ResolveCapabilityAction(ctx, "ipam"); err == nil {
		t.Fatal("and it must not resolve")
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
	r.manifest = func(_ context.Context, _ string) (PluginManifest, error) {
		if blip {
			return PluginManifest{}, errors.New("dial blip")
		}
		return PluginManifest{Capabilities: []string{"keycustodian"}}, nil
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

// ── dial-less providers (ADR-0138 D5) ────────────────────────────────────────

// The verdict a subprocess provider could never reach. Verification meant fetching a Manifest
// over a dial address, and an EE-Job Actuator has none by construction — so `configmgmt`, whose
// first provider is a subprocess BY CHARTER (§3, GPLv3), was structurally unroutable. The claim
// is now corroborated against the DECLARED mechanisms, and labelled as the weaker thing it is.
func TestDialLessProviderIsAttestedFromItsDeclaration(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())
	r.manifest = func(context.Context, string) (PluginManifest, error) {
		t.Fatal("a dial-less provider must not be dialed — there is no Manifest to fetch")
		return PluginManifest{}, nil
	}

	if err := s.UpsertActuator(ctx, types.Actuator{
		Name: "t-ansible-cm", PluginIdentity: "ansible", JobCommand: []string{"stratt-ansible"},
		Provides: []string{"configmgmt"}, Remediates: map[string]string{"Application": "web-server-configure"},
	}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-ansible-cm")
	defer s.DeleteProviderVerification(ctx, "actuator", "t-ansible-cm")

	r.ReconcileProviderVerification(ctx)
	row, ok := verificationRow(t, s, "actuator", "t-ansible-cm")
	if !ok || !row.Verified {
		t.Fatalf("a dial-less provider that declares a mechanism must count: %+v ok=%v", row, ok)
	}
	if row.Basis != basisDeclaration {
		t.Fatalf("basis must record that a DECLARATION was read, not a running binary — two verdicts "+
			"that both read verified=true are not equally strong (§1.8): got %q", row.Basis)
	}
	// And it satisfies a consumer's requirement, which is the entire point.
	res, err := r.buildProviderIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := classifyRequires([]string{"configmgmt"}, res); !ok {
		t.Fatalf("an attested provider must satisfy a requirement: %s", reason)
	}
}

// The refusal that survives D5: a class claim with nothing behind it. Without this, "dial-less is
// verifiable" would degrade to "dial-less is self-certifying".
func TestDialLessProviderWithNoMechanismStaysRefused(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())

	if err := s.UpsertActuator(ctx, types.Actuator{
		Name: "t-bare-cm", PluginIdentity: "ansible", JobCommand: []string{"stratt-ansible"},
		Provides: []string{"configmgmt"},
	}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-bare-cm")
	defer s.DeleteProviderVerification(ctx, "actuator", "t-bare-cm")

	r.ReconcileProviderVerification(ctx)
	row, ok := verificationRow(t, s, "actuator", "t-bare-cm")
	if !ok || row.Verified {
		t.Fatalf("a bare class claim must not count: %+v ok=%v", row, ok)
	}
	if !strings.Contains(row.Reason, "no mechanism") {
		t.Fatalf("the reason must say what is missing (§1.8): %q", row.Reason)
	}
}

// An attested provider CANNOT serve an Action-shaped class, and must fail closed saying so. The
// implementation mapping lives in the Manifest (ADR-0140 D1) and a subprocess has no way to supply
// one — so attestation admits the Workflow-shaped classes and stops honestly at the others. A
// design that let this resolve would have invented a name, which is what ADR-0140 deleted.
func TestAttestedProviderCannotServeAnActionShapedClass(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())

	if err := s.UpsertActuator(ctx, types.Actuator{
		Name: "t-sub-ipam", PluginIdentity: "subproc", JobCommand: []string{"stratt-thing"},
		Provides: []string{"ipam"}, ActionNames: []string{"subproc/ipam-resolve"},
	}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-sub-ipam")
	defer s.DeleteProviderVerification(ctx, "actuator", "t-sub-ipam")

	r.ReconcileProviderVerification(ctx)
	if row, ok := verificationRow(t, s, "actuator", "t-sub-ipam"); !ok || !row.Verified {
		t.Fatalf("it attests (it declares a mechanism): %+v ok=%v", row, ok)
	}
	_, err := r.ResolveCapabilityAction(ctx, "ipam")
	if err == nil {
		t.Fatal("but an Action-shaped class must NOT resolve — the implementation mapping lives in a " +
			"Manifest the provider has no way to advertise")
	}
	if !strings.Contains(err.Error(), "advertises no Action implementing the class") {
		t.Fatalf("the diagnostic must point at the missing advertisement: %v", err)
	}
}

// ResolveCapabilityActuator is the Actuator-shaped resolution a capability-typed reconcile uses
// (ADR-0140 D4). It reads NO advertisement, and that asymmetry is the design: an Action-shaped
// class needs `implements` because the plugin owns the Action's name inside its own namespace,
// while here the provider DECLARATION IS the Actuator — its name is the operator's, granted in
// CaC, and core derives nothing.
func TestResolveCapabilityActuator(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())
	r.manifest = fakeManifest(map[string][]string{"localhost:9096": {"certissuer"}})

	if _, err := r.ResolveCapabilityActuator(ctx, "certissuer"); err == nil {
		t.Fatal("no provider must fail closed")
	}

	if err := s.UpsertActuator(ctx, types.Actuator{
		Name: "t-cert-issuer", Address: "localhost:9096", PluginIdentity: "openbao",
		Provides: []string{"certissuer"}, FacetNamespaces: []string{"cert.identity", "cert.expiry"},
	}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteActuator(ctx, "t-cert-issuer")
	defer s.DeleteProviderVerification(ctx, "actuator", "t-cert-issuer")

	// Declared but unverified must not bind.
	if _, err := r.ResolveCapabilityActuator(ctx, "certissuer"); err == nil {
		t.Fatal("a declared-but-unverified provider must not resolve")
	}
	r.ReconcileProviderVerification(ctx)
	got, err := r.ResolveCapabilityActuator(ctx, "certissuer")
	if err != nil {
		t.Fatalf("a verified Actuator provider must resolve: %v", err)
	}
	if got != "t-cert-issuer" {
		t.Fatalf("resolution returns the declaration name, which IS the dispatch-table Actuator: %q", got)
	}
}

// A Connector may advertise a class, but this form resolves to something DISPATCHABLE and a
// Connector is not. Binding one would hand the reconcile an Actuator name that is not in the
// dispatch table — a failure at fire time, six hours later.
func TestResolveCapabilityActuatorIgnoresConnectors(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plugins := orchestrate.NewPluginRegistry(nil, nil)
	r := New(s, plugins, homegate.Deps{}, nil, lazyDial, time.Second, discard())
	r.manifest = fakeManifest(map[string][]string{"localhost:9097": {"certissuer"}})

	if err := s.UpsertConnector(ctx, types.Connector{
		Name: "t-cert-conn", Class: types.ConnectorSyncer, Address: "localhost:9097", PluginIdentity: "cc",
		Source: types.Source{Kind: "cc", Name: "t-cert-conn"}, Provides: []string{"certissuer"},
	}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteConnector(ctx, "t-cert-conn")
	defer s.DeleteProviderVerification(ctx, "connector", "t-cert-conn")

	r.ReconcileProviderVerification(ctx)
	if v, ok := verificationRow(t, s, "connector", "t-cert-conn"); !ok || !v.Verified {
		t.Fatalf("the Connector verifies as a provider of the class: %+v ok=%v", v, ok)
	}
	if _, err := r.ResolveCapabilityActuator(ctx, "certissuer"); err == nil {
		t.Fatal("but it must NOT satisfy an ACTUATION — a Connector is not dispatchable")
	}
}
