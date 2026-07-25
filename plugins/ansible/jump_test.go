package ansible

import (
	"strings"
	"testing"
)

// The decision, rendered (ADR-0126 D3): the core resolves the reached-via chain into
// coordinates, and the SHIM turns them into ssh's ProxyJump. There is no ssh flag
// anywhere in core (§1.4, ADR-0084 D3) — this is the only place one is authored.
func TestProxyJumpRendersTheResolvedChain(t *testing.T) {
	vars, err := connectionVars(
		&connectionParams{User: "appops", Jump: []connectionAuth{{User: "jump"}}},
		[]Hop{{Name: "bastion", Address: "10.0.0.9", Port: 2222}},
		"/runner/known_hosts", oneKey, fakeStage)
	if err != nil {
		t.Fatal(err)
	}
	args := vars["ansible_ssh_common_args"]
	if !strings.Contains(args, "ProxyJump=jump@10.0.0.9:2222") {
		t.Fatalf("the hop must render as [user@]host[:port], got %q", args)
	}
}

// Order is load-bearing and is NOT re-derived here: ssh reads -J nearest-first, which is
// the order the core resolved. A silently-reversed chain would connect through the wrong
// box and still look like it worked.
func TestProxyJumpPreservesNearestFirstOrder(t *testing.T) {
	vars, err := connectionVars(&connectionParams{}, []Hop{
		{Name: "edge", Address: "10.0.0.9"},
		{Name: "inner", Address: "10.1.0.9"},
	}, "", oneKey, fakeStage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(vars["ansible_ssh_common_args"], "ProxyJump=10.0.0.9,10.1.0.9") {
		t.Fatalf("hops must stay nearest-first: %q", vars["ansible_ssh_common_args"])
	}
}

// A shorter auth array reuses its last entry — "same jump credential for every hop",
// declared once. An empty array is legitimate (agent-forwarded / certificate bastion).
func TestHopAuthReusesTheLastEntry(t *testing.T) {
	auth := []connectionAuth{{User: "jump"}}
	if got := hopAuth(auth, 3).User; got != "jump" {
		t.Errorf("a short array must reuse its last entry, got %q", got)
	}
	if got := hopAuth(nil, 0).User; got != "" {
		t.Errorf("no per-hop auth is legitimate, got %q", got)
	}
}

// The staged key filename is derived from its MOUNT. With a fixed name the target key
// and every hop key overwrote one another and the last one written silently became the
// key for all of them — a bug this test exists because I wrote.
func TestStagedKeysDoNotCollide(t *testing.T) {
	// Two different refs must produce two different destinations. The source files do
	// not exist here — stageKeyIn fails on the read — but the DESTINATION is computed
	// before the read, and the destination is what collided.
	dir := t.TempDir()
	if a, b := stagedPathFor(dir, credentialsMount+"/node-key/id_ed25519"),
		stagedPathFor(dir, credentialsMount+"/bastion-key/id_ed25519"); a == b {
		t.Fatalf("two credentials stage to the same path %q — the second silently wins", a)
	}
}

// One Run renders ONE ProxyJump (the vars are inventory group vars), so a slice whose
// targets sit behind DIFFERENT bastions cannot be rendered correctly. It is refused,
// naming both offenders — because the alternative, rendering one chain, would route the
// other targets through the wrong bastion, and returning no chain would connect them
// DIRECT, silently ignoring a declared bastion entirely (§1.8/§2.4).
func TestDisagreeingChainsAreRefusedNotSilentlyDropped(t *testing.T) {
	targets := []Target{
		{Name: "web1", Jump: []Hop{{Address: "10.0.0.9"}}},
		{Name: "web2", Jump: []Hop{{Address: "10.0.0.10"}}},
	}
	_, err := jumpChainOf(targets)
	if err == nil {
		t.Fatal("targets behind different bastions must be refused, never silently connected direct")
	}
	for _, want := range []string{"web1", "web2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name both offenders, missing %q: %v", want, err)
		}
	}
	// Agreeing targets (the normal case) resolve to the shared chain.
	same := []Target{
		{Name: "web1", Jump: []Hop{{Address: "10.0.0.9"}}},
		{Name: "web2", Jump: []Hop{{Address: "10.0.0.9"}}},
	}
	chain, err := jumpChainOf(same)
	if err != nil || len(chain) != 1 {
		t.Fatalf("a shared chain must resolve: %v %v", chain, err)
	}
	// And no chain at all stays the common, unbastioned case.
	if chain, err := jumpChainOf([]Target{{Name: "web1"}}); err != nil || chain != nil {
		t.Fatalf("an unbastioned slice must render no ProxyJump: %v %v", chain, err)
	}
}

// A hop with no address is refused at the shim too, not only at resolve time. Emitting a
// spec without it would drop the hop and connect DIRECT — the failure mode the whole
// decision exists to prevent, so both layers refuse it.
func TestHopWithoutAddressIsRefused(t *testing.T) {
	_, err := connectionVars(&connectionParams{}, []Hop{{Name: "ghost"}}, "", oneKey, fakeStage)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("an addressless hop must be refused by name, got %v", err)
	}
}

// No chain ⇒ no ProxyJump at all. The overwhelmingly common case must render exactly
// what it rendered before D3.
func TestNoChainRendersNoProxyJump(t *testing.T) {
	vars, err := connectionVars(&connectionParams{User: "appops"}, nil, "/runner/known_hosts", oneKey, fakeStage)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(vars["ansible_ssh_common_args"], "ProxyJump") {
		t.Fatalf("an unbastioned target must render no ProxyJump: %q", vars["ansible_ssh_common_args"])
	}
}
