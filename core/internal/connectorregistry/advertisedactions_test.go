package connectorregistry

import (
	"context"
	"errors"
	"strings"
	"testing"
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
		{Name: "notify/smtp", InputContract: "actions/notify/smtp.input"},
		{Name: "notify/webhook", InputContract: "actions/notify/webhook.input"},
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
		{Name: "helm/deploy", InputContract: "actions/helm/nosuch.input"},
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
		{Name: "helm/deploy", InputContract: "actions/helm/deploy.input", OutputContract: "actions/helm/deploy.output"},
		{Name: "notify/smtp", InputContract: "actions/notify/smtp.input"}, // no typed outputs
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
			{Name: "helm/deploy", InputContract: "actions/helm/deploy.input"},
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
