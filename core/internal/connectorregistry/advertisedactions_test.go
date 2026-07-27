package connectorregistry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/contract"
)

// fakeActions is a ManifestFetcher backed by a static addr→advertised-Actions map — the
// actionNames counterpart to fakeManifest's capability map.
func fakeActions(m map[string][]AdvertisedAction) ManifestFetcher {
	return func(_ context.Context, addr string) (PluginManifest, error) {
		return PluginManifest{Actions: m[addr]}, nil
	}
}

// The estate GRANTS an Action name into the dispatch table; the Manifest is the plugin's own
// account of what it ships. Until now only `provides` was checked against the Manifest —
// `actionNames` was taken on the estate's word, so a name the plugin never claimed registered
// fine and failed at Invoke, in a Run rather than in a diff (§1.5, §1.8).

func TestUnadvertisedActionNameIsRefused(t *testing.T) {
	err := checkAdvertisedActions([]string{"helm/deploy"}, PluginManifest{})
	if err == nil {
		t.Fatal("an actionName the plugin's Manifest does not advertise must be refused — registering it " +
			"means core minted a dispatch entry on the estate's word alone")
	}
	// §1.8: "no such Action" is only actionable next to what the plugin DID advertise.
	if !strings.Contains(err.Error(), "advertised: none") {
		t.Fatalf("the diagnostic must name the advertised set: %v", err)
	}
}

func TestUnadvertisedActionNameNamesTheAlternatives(t *testing.T) {
	m := PluginManifest{Actions: []AdvertisedAction{
		{Name: "notify/smtp", InputContract: "actions/cert-issuer/rotate-crl.input"},
		{Name: "notify/webhook", InputContract: "actions/adopt/materialize.input"},
	}}
	err := checkAdvertisedActions([]string{"notify/websocket"}, m)
	if err == nil {
		t.Fatal("a near-miss name must still be refused")
	}
	if !strings.Contains(err.Error(), "notify/smtp, notify/webhook") {
		t.Fatalf("a typo is only diagnosable next to the real names: %v", err)
	}
}

// The ID-level half of the Contract cross-check: a plugin conformance-checking args against a
// document core does not hold means the two ends of the seam cannot be shown to agree. This is
// the check hash-equality (port invariant #5) is built on top of — comparing hashes of a
// document core cannot resolve is not possible.
func TestAdvertisedContractCoreDoesNotHoldIsRefused(t *testing.T) {
	m := PluginManifest{Actions: []AdvertisedAction{
		{Name: "helm/deploy", InputContract: "actions/cert-issuer/nosuch.input"},
	}}
	err := checkAdvertisedActions([]string{"helm/deploy"}, m)
	if err == nil {
		t.Fatal("an advertised Contract id core cannot resolve must be refused")
	}
	if !strings.Contains(err.Error(), "core does not hold") {
		t.Fatalf("the diagnostic must say which side is missing the document: %v", err)
	}
}

func TestAdvertisedActionWithNoInputContractIsRefused(t *testing.T) {
	m := PluginManifest{Actions: []AdvertisedAction{{Name: "helm/deploy"}}}
	if err := checkAdvertisedActions([]string{"helm/deploy"}, m); err == nil {
		t.Fatal("an Action advertising no input Contract is an uncontracted operation surface (§2.2, ADR-0031)")
	}
}

// The counterweight, and the shape every shipped plugin is in. An empty OUTPUT id is lawful —
// the port defines it as "empty if the Action returns no typed outputs" — so it must not be
// mistaken for a missing document.
func TestAdvertisedActionsThatResolveAreAccepted(t *testing.T) {
	m := PluginManifest{Actions: []AdvertisedAction{
		{Name: "helm/deploy", InputContract: "actions/cert-issuer/create-intermediate.input", OutputContract: "actions/cert-issuer/create-intermediate.output"},
		{Name: "notify/smtp", InputContract: "actions/cert-issuer/rotate-crl.input"}, // no typed outputs
	}}
	if err := checkAdvertisedActions([]string{"helm/deploy", "notify/smtp"}, m); err != nil {
		t.Fatalf("declared actionNames the plugin advertises against Contracts core holds must be accepted: %v", err)
	}
}

// A capability-implementing Action advertises the CLASS contract, not `actions/<name>.input`
// (ADR-0112 D2 — a capability call is validated against the class). That divergence is lawful
// and must pass: this check tests that an advertised id RESOLVES, never that it equals what
// core's own convention would compute. Which divergences are lawful is the fact ADR-0140 D1
// makes declared; until then, asserting equality here would refuse netbox.
func TestCapabilityImplementingActionAdvertisesTheClassContract(t *testing.T) {
	m := PluginManifest{Actions: []AdvertisedAction{
		{Name: "netbox/ipam-resolve", InputContract: "capabilities/ipam.input", OutputContract: "capabilities/ipam.output"},
	}}
	if err := checkAdvertisedActions([]string{"netbox/ipam-resolve"}, m); err != nil {
		t.Fatalf("a capability-implementing Action advertising its class Contract must be accepted: %v", err)
	}
}

// Nothing declared → no Manifest round-trip. Most Actuators expose no targetless Action and
// must not pay a fetch to enable.
func TestNoDeclaredActionsSkipsTheManifestFetch(t *testing.T) {
	r := &Registry{}
	r.manifest = func(context.Context, string) (PluginManifest, error) {
		t.Fatal("an Actuator declaring no actionNames must not fetch a Manifest")
		return PluginManifest{}, nil
	}
	if err := r.verifyDeclaredActions(context.Background(), "localhost:9090", nil); err != nil {
		t.Fatal(err)
	}
}

// Enabling now requires the plugin to ANSWER, which is a real behavior change: a lazy gRPC dial
// succeeds against a service that does not exist, so before the actionNames check an Actuator whose
// pod was absent registered into the dispatch table and reported `enabled: true` while being
// completely unusable. On the connector-e2e floor three of them did exactly that.
//
// The honest version must be LEVEL-TRIGGERED, not latching. An Actuator held back because its
// plugin was not up yet has to enable on its own once the plugin answers — no restart, no manual
// step — or the check trades a §1.8 lie for an operational trap.
func TestActuatorHeldBackByAnUnreachablePluginEnablesWhenItAnswers(t *testing.T) {
	up := false
	r := &Registry{}
	r.manifest = func(context.Context, string) (PluginManifest, error) {
		if !up {
			return PluginManifest{}, errors.New("produced zero addresses")
		}
		return PluginManifest{Actions: []AdvertisedAction{
			{Name: "helm/deploy", InputContract: "actions/cert-issuer/create-intermediate.input"},
		}}, nil
	}

	err := r.verifyDeclaredActions(context.Background(), "stratt-helm:9090", []string{"helm/deploy"})
	if err == nil {
		t.Fatal("an unreachable plugin must hold its Actuator back rather than register a name nothing serves")
	}
	if !strings.Contains(err.Error(), "manifest fetch") {
		t.Fatalf("the status must say the plugin could not be asked, not that the Action is wrong: %v", err)
	}

	up = true // the pod rolls out
	if err := r.verifyDeclaredActions(context.Background(), "stratt-helm:9090", []string{"helm/deploy"}); err != nil {
		t.Fatalf("the next reconcile must enable it with no restart — enable is retried while the entry "+
			"is absent, the same level-triggered convergence the dependency gate uses: %v", err)
	}
}

// ── hash equality (port invariant #5) ────────────────────────────────────────
//
// The loop this whole arc was opening. A plugin conformance-checks args against ITS copy of a
// schema; core validates the Step against ITS copy. Nothing compared them, and nothing could
// while the document lived in the core binary — the plugin had nothing to hash. Since ADR-0138
// D4 it ships with the plugin, so the plugin can pin the bytes it will enforce.

// heldHash is the digest core holds for a contract — the value a plugin must agree with.
func heldHash(t *testing.T, id string) string {
	t.Helper()
	c, ok, err := contract.Get(id)
	if err != nil || !ok {
		t.Fatalf("core must hold %q for this test to mean anything: ok=%v err=%v", id, ok, err)
	}
	return c.Hash
}

func TestMatchingPinIsAccepted(t *testing.T) {
	id := "actions/cert-issuer/create-intermediate.input"
	m := PluginManifest{Actions: []AdvertisedAction{
		{Name: "cert-issuer/create-intermediate", InputContract: id, InputSha: heldHash(t, id)},
	}}
	if err := checkAdvertisedActions([]string{"cert-issuer/create-intermediate"}, m); err != nil {
		t.Fatalf("a plugin pinning the same bytes core holds must be accepted: %v", err)
	}
}

// THE check. A plugin enforcing different bytes than core validates against means a Step can pass
// the load and fail at the plugin — or worse, pass both against divergent rules.
func TestDivergentPinIsRefused(t *testing.T) {
	id := "actions/cert-issuer/create-intermediate.input"
	m := PluginManifest{Actions: []AdvertisedAction{
		{Name: "cert-issuer/create-intermediate", InputContract: id,
			InputSha: "0000000000000000000000000000000000000000000000000000000000000000"},
	}}
	err := checkAdvertisedActions([]string{"cert-issuer/create-intermediate"}, m)
	if err == nil {
		t.Fatal("a pin that disagrees with core's copy is schema drift and must be refused")
	}
	for _, want := range []string{"DIFFERENT BYTES", "000000000000", heldHash(t, id)[:12]} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnostic must show both digests so the reader can tell WHICH side moved: missing %q in %v", want, err)
		}
	}
}

// Unpinned means "no claim", and it is the lawful state for a SEAM the plugin does not own — a
// capability class contract, or a neutrally-named one core keeps because several vendors may
// implement it. Refusing these would make every capability-implementing Action unregisterable.
func TestUnpinnedSeamRefIsAccepted(t *testing.T) {
	m := PluginManifest{Actions: []AdvertisedAction{
		{Name: "netbox/ipam-resolve", InputContract: "capabilities/ipam.input",
			OutputContract: "capabilities/ipam.output"}, // no sha: netbox does not own the class contract
	}}
	if err := checkAdvertisedActions([]string{"netbox/ipam-resolve"}, m); err != nil {
		t.Fatalf("a plugin cannot hash a seam it does not ship; unpinned must stay lawful: %v", err)
	}
}

// A pin is checked on the OUTPUT ref too — an Action that lies about the shape it returns is the
// direction that corrupts a downstream Step's {{.steps.x.outputs.y}} binding.
func TestDivergentOutputPinIsRefused(t *testing.T) {
	in := "actions/cert-issuer/create-intermediate.input"
	out := "actions/cert-issuer/create-intermediate.output"
	m := PluginManifest{Actions: []AdvertisedAction{
		{Name: "cert-issuer/create-intermediate", InputContract: in, InputSha: heldHash(t, in),
			OutputContract: out, OutputSha: "deadbeef"},
	}}
	if err := checkAdvertisedActions([]string{"cert-issuer/create-intermediate"}, m); err == nil {
		t.Fatal("a divergent OUTPUT pin must be refused too")
	}
}
