package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// fakeGraph is the narrow slice of the store a chain walk reads. It cannot write, which
// is the point of the interface being narrow (§1.2).
type fakeGraph struct {
	edges map[string][]string // fromID → reached-via targets
	addr  map[string]string   // entityID → mgmt.address address
	miss  map[string]bool     // entityID → GetEntity fails
}

func (f fakeGraph) RelationTargets(_ context.Context, from, rel string) ([]string, error) {
	if rel != types.RelReachedVia {
		return nil, nil
	}
	return f.edges[from], nil
}

func (f fakeGraph) GetEntity(_ context.Context, id string) (types.Entity, error) {
	if f.miss[id] {
		return types.Entity{}, fmt.Errorf("no such entity %s", id)
	}
	return types.Entity{ID: id, Labels: map[string]string{"graph.name": id}}, nil
}

func (f fakeGraph) FacetValuesByEntities(_ context.Context, ns string, ids []string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if ns != "mgmt.address" {
		return out, nil
	}
	for _, id := range ids {
		if a, ok := f.addr[id]; ok {
			out[id] = json.RawMessage(`{"address":"` + a + `"}`)
		}
	}
	return out, nil
}

// The decision (ADR-0126 D3): a jump host is a Relation to an Entity, and each hop's
// coordinate is read from THAT Entity's own mgmt.address. Nothing is copied onto the
// target, so a bastion's address has exactly one home and cannot drift from the graph's
// (§2.4) — which is why this is a Relation and not a field grown onto the closed
// mgmt.address schema (§9).
func TestChainReadsEachHopsOwnAddress(t *testing.T) {
	g := fakeGraph{
		edges: map[string][]string{"web1": {"edge"}, "edge": {"inner"}},
		addr:  map[string]string{"web1": "10.9.9.9", "edge": "10.0.0.9", "inner": "10.1.0.9"},
	}
	hops, err := resolveJumpChain(context.Background(), g, "web1")
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) != 2 {
		t.Fatalf("both hops must resolve, got %+v", hops)
	}
	// Nearest first — the order ssh's -J consumes, so nothing downstream re-derives it.
	if hops[0].Address != "10.0.0.9" || hops[1].Address != "10.1.0.9" {
		t.Errorf("hops must be nearest-first with their OWN addresses: %+v", hops)
	}
	if hops[0].Name != "edge" {
		t.Errorf("a hop carries its name so a failure can say WHICH bastion: %+v", hops[0])
	}
}

// The common case stays free: no reached-via edge ⇒ no chain, no error.
func TestNoChainIsTheNormalCase(t *testing.T) {
	g := fakeGraph{addr: map[string]string{"web1": "10.9.9.9"}}
	hops, err := resolveJumpChain(context.Background(), g, "web1")
	if err != nil || hops != nil {
		t.Fatalf("an unbastioned host must resolve to no chain: %+v %v", hops, err)
	}
}

// A cycle would otherwise walk forever. Caught explicitly rather than left to the hop
// bound, so the diagnosis says "cycles" instead of "too many hops" (§1.8).
func TestCycleIsRefused(t *testing.T) {
	g := fakeGraph{
		edges: map[string][]string{"web1": {"a"}, "a": {"b"}, "b": {"a"}},
		addr:  map[string]string{"a": "10.0.0.1", "b": "10.0.0.2"},
	}
	_, err := resolveJumpChain(context.Background(), g, "web1")
	if err == nil || !strings.Contains(err.Error(), "cycles") {
		t.Fatalf("a cycle must be refused and named, got %v", err)
	}
}

// THE load-bearing failure: a hop with no address must NOT fall back to connecting
// directly. A target declared to sit behind a bastion that is then reached around it is
// worse than one that fails to be reached at all — same rule as ADR-0084 D2's
// no-silent-localhost (§1.8).
func TestHopWithoutAddressFailsRatherThanConnectingDirect(t *testing.T) {
	g := fakeGraph{
		edges: map[string][]string{"web1": {"ghost"}},
		addr:  map[string]string{"web1": "10.9.9.9"}, // ghost has none
	}
	_, err := resolveJumpChain(context.Background(), g, "web1")
	if err == nil {
		t.Fatal("a bastion with no address must fail the resolve, never be skipped")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("the failure must name the hop: %v", err)
	}
}

// Two bastions for one host is an ambiguity core will not resolve: picking one would be
// exactly the implicit precedence §2.4 forbids, and there is deliberately no tiebreak
// field to add.
func TestAmbiguousChainIsRefused(t *testing.T) {
	g := fakeGraph{
		edges: map[string][]string{"web1": {"a", "b"}},
		addr:  map[string]string{"a": "10.0.0.1", "b": "10.0.0.2"},
	}
	_, err := resolveJumpChain(context.Background(), g, "web1")
	if err == nil || !strings.Contains(err.Error(), "ONE bastion") {
		t.Fatalf("two hops from one host must be refused, got %v", err)
	}
}
