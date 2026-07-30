package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Which Contract governs an Action Run is decided by HOW the Step named it (ADR-0112 D2 /
// ADR-0140 D3 row 2): a class-named Step is governed by the class, so a provider swap changes no
// consumer's expectation. Two call sites derive this name — the expectation sent to the plugin and
// the post-hoc output validation — and they must not be able to disagree.
func TestActionOutputContractFollowsHowTheStepNamedIt(t *testing.T) {
	named := RunInput{Action: "netbox/ipam-resolve"}
	if got := actionOutputContract(named); got != "actions/netbox/ipam-resolve.output" {
		t.Errorf("a named Action is governed by its own Contract, got %q", got)
	}
	// Same resolved Action, reached through the class → the CLASS Contract governs.
	viaClass := RunInput{Action: "netbox/ipam-resolve", ActionCapability: "ipam"}
	if got := actionOutputContract(viaClass); got != "capabilities/ipam.output" {
		t.Errorf("a class-named Step is governed by the class Contract, got %q", got)
	}
}

// Resolution failures are terminal, not retryable: no verified provider, an ambiguous pair, and a
// provider advertising no implementation are all fixed by changing a declaration or a plugin —
// never by trying again. Retrying would bury the diagnosis under attempts (§1.8).
func TestResolveActionCapabilityFailsTerminally(t *testing.T) {
	a := &Activities{}
	// No resolver configured at all — the shape a floor is in when the runtime registry is off.
	if _, err := a.ResolveActionCapability(context.Background(), "ipam"); err == nil {
		t.Fatal("a Step naming a capability with no resolver configured must fail visibly")
	} else if !strings.Contains(err.Error(), "no capability resolver") {
		t.Fatalf("the diagnostic must say the resolver is missing, not that ipam is unknown: %v", err)
	}

	a.ResolveCapability = func(context.Context, string) (string, error) {
		return "", errors.New("no verified provider for capability \"ipam\"")
	}
	_, err := a.ResolveActionCapability(context.Background(), "ipam")
	if err == nil {
		t.Fatal("an unresolvable capability must fail the Step")
	}
	// The provider-side reason must survive to the Run — it is the descent pointer.
	if !strings.Contains(err.Error(), "no verified provider") {
		t.Fatalf("the resolver's reason must not be swallowed: %v", err)
	}
}

func TestResolveActionCapabilityReturnsTheAdvertisedName(t *testing.T) {
	a := &Activities{ResolveCapability: func(_ context.Context, class string) (string, error) {
		if class != "ipam" {
			t.Fatalf("class = %q", class)
		}
		return "netbox/allocate-prefix", nil // a name the deleted convention could not produce
	}}
	got, err := a.ResolveActionCapability(context.Background(), "ipam")
	if err != nil {
		t.Fatal(err)
	}
	if got != "netbox/allocate-prefix" {
		t.Fatalf("the advertised name must be carried opaquely, got %q", got)
	}
}

// TestCapabilityArgsAreBuiltPerClass pins NET-4's core half: the resolve request is DECLARED per
// capability class, not one shape reused for all of them.
//
// The defect it prevents, measured on a live floor: resolveCapabilities marshalled
// `{"workspace": …}` for every class and validated it against that class's Contract. statestore
// (required: [workspace]) fit; `ipam` could not, and a launched subnet build died at
//
//	capability "ipam" resolve input failed its Contract: missing properties 'key', 'size';
//	additional properties 'workspace' not allowed
//
// A generic seam exercised by exactly one consumer — the shape of defect this arc kept finding.
// ADR-0111 had booked the fix as the "Intent-declares-the-request param-flow" follow-up; it
// arrived as a build that could not run.
func TestCapabilityArgsAreBuiltPerClass(t *testing.T) {
	req := map[string]map[string]any{
		"ipam":       {"key": "app-subnet", "size": 24, "pool": "10.30.0.0/16"},
		"statestore": {"workspace": "app-subnet"},
	}
	a := &Activities{}
	got, err := a.ResolveCapabilityArgs(context.Background(), req, nil, nil, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// Each class keeps its OWN request. The failure this guards is one shape leaking across classes.
	if got["ipam"]["size"] != 24 || got["ipam"]["pool"] != "10.30.0.0/16" {
		t.Fatalf("the ipam request must survive intact, got %v", got["ipam"])
	}
	if _, leaked := got["ipam"]["workspace"]; leaked {
		t.Fatal("statestore's `workspace` must NOT appear in the ipam request — one shape for every " +
			"class is exactly what made ipam unresolvable")
	}
	if got["statestore"]["workspace"] != "app-subnet" {
		t.Fatalf("statestore keeps its own shape, got %v", got["statestore"])
	}
}

// TestCapabilityArgsSubstituteFromLaunch: the request is DESIRED STATE and reaches the provider
// through the launch interface, which is what makes it the Intent's to declare rather than the
// Actuator's. `requires:` names the classes a tool needs; the size of one subnet is not a property
// of the tool.
func TestCapabilityArgsSubstituteFromLaunch(t *testing.T) {
	a := &Activities{}
	got, err := a.ResolveCapabilityArgs(context.Background(),
		map[string]map[string]any{"ipam": {"key": "{{.launch.name}}", "size": "{{.launch.params.size}}"}},
		nil, nil,
		map[string]any{"name": "app-subnet", "params": map[string]any{"size": 24}},
	)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got["ipam"]["key"] != "app-subnet" {
		t.Fatalf("launch binding not substituted: %v", got["ipam"])
	}
	// An exact single-token binding PRESERVES the value's type — `size` must stay a number, because
	// capabilities/ipam.input types it as one and nothing coerces between types (ADR-0118 D1).
	if got["ipam"]["size"] != 24 {
		t.Fatalf("size must survive as a number, got %#v", got["ipam"]["size"])
	}
}
