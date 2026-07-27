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
