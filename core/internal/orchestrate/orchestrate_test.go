package orchestrate

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/types"
)

// TestSiteReachableFromCell pins the slice-6 Site→Cell binding decision: a Site
// is reachable only from its own Cell (its dispatch queue lives on that Cell's
// NATS); an unset Site cell is co-located; a single-Cell 'local' estate treats
// every Site as reachable (no-op).
func TestSiteReachableFromCell(t *testing.T) {
	cases := []struct {
		siteCell, daemonCell string
		want                 bool
	}{
		{"", "local", true},    // unset Site, local daemon — co-located
		{"", "eu", true},       // unset Site, named daemon — co-located
		{"eu", "eu", true},     // same Cell — reachable
		{"eu", "us", false},    // peer Cell — its queue is on another NATS
		{"eu", "local", false}, // named Site, local daemon — unreachable
		{"local", "eu", false}, // explicit 'local' Site, eu daemon — unreachable
	}
	for _, c := range cases {
		if got := siteReachableFromCell(c.siteCell, c.daemonCell); got != c.want {
			t.Errorf("siteReachableFromCell(%q,%q)=%v want %v", c.siteCell, c.daemonCell, got, c.want)
		}
	}
}

func mkTargets(n int) []actuators.Target {
	out := make([]actuators.Target, n)
	for i := range out {
		out[i] = actuators.Target{EntityID: fmt.Sprintf("e-%d", i), Name: fmt.Sprintf("t-%d", i)}
	}
	return out
}

func TestSplitTargets(t *testing.T) {
	cases := []struct {
		targets, slices int
		wantChunks      []int
	}{
		{10, 1, []int{10}},
		{10, 3, []int{4, 3, 3}},
		{10, 10, []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}},
		{3, 8, []int{1, 1, 1}}, // slices clamp to target count
		{5, 0, []int{5}},       // 0/negative clamps to 1
		{7, 2, []int{4, 3}},
	}
	for _, c := range cases {
		chunks := splitTargets(mkTargets(c.targets), c.slices)
		if len(chunks) != len(c.wantChunks) {
			t.Fatalf("targets=%d slices=%d: got %d chunks, want %d", c.targets, c.slices, len(chunks), len(c.wantChunks))
		}
		seen := map[string]bool{}
		total := 0
		for i, ch := range chunks {
			if len(ch) != c.wantChunks[i] {
				t.Fatalf("targets=%d slices=%d chunk %d: len %d, want %d", c.targets, c.slices, i, len(ch), c.wantChunks[i])
			}
			for _, tgt := range ch {
				if seen[tgt.EntityID] {
					t.Fatalf("target %s appears in two chunks", tgt.EntityID)
				}
				seen[tgt.EntityID] = true
				total++
			}
		}
		if total != c.targets {
			t.Fatalf("chunks lose targets: %d != %d", total, c.targets)
		}
	}
}

func TestMergeResults(t *testing.T) {
	merged := mergeResults([]dispatch.Result{
		{Succeeded: true, PerTarget: map[string]string{"a": "ok", "b": "changed"}, SpawnLatency: 500 * time.Millisecond},
		{Succeeded: false, PerTarget: map[string]string{"c": "failed"}, SpawnLatency: 900 * time.Millisecond},
	})
	if merged.Succeeded {
		t.Fatal("one failed slice must fail the merge")
	}
	if merged.PerTarget["a"] != "ok" || merged.PerTarget["b"] != "changed" || merged.PerTarget["c"] != "failed" {
		t.Fatalf("per-target union: %+v", merged.PerTarget)
	}
	if merged.SpawnLatency != 900*time.Millisecond {
		t.Fatalf("spawn latency must report the slowest slice, got %s", merged.SpawnLatency)
	}
}

// TestObservedNameIsToolBlind locks the ADR-0089 cleanup: the target namer reads the tool-blind
// "<source>.name" label CONVENTION (never a hardcoded tool key), picks deterministically, and
// falls back to the entity id.
func TestObservedNameIsToolBlind(t *testing.T) {
	// Any source's <src>.name works — not just one tool.
	if got := observedName(types.Entity{ID: "e1", Labels: map[string]string{"aws.name": "web-01"}}); got != "web-01" {
		t.Fatalf("aws.name: got %q want web-01", got)
	}
	if got := observedName(types.Entity{ID: "e2", Labels: map[string]string{"vcenter.name": "vm-9"}}); got != "vm-9" {
		t.Fatalf("vcenter.name: got %q want vm-9", got)
	}
	// Deterministic pick (alphabetically-first key) when multiple sources name it.
	multi := types.Entity{ID: "e3", Labels: map[string]string{"vcenter.name": "z", "aws.name": "a"}}
	if got := observedName(multi); got != "a" {
		t.Fatalf("multi-source pick must be deterministic (aws.name < vcenter.name): got %q", got)
	}
	// Fallback to the stable entity id when no source stamped a name.
	if got := observedName(types.Entity{ID: "e4", Labels: map[string]string{"mgmt.site": "x"}}); got != "e4" {
		t.Fatalf("fallback: got %q want e4", got)
	}
}

// TestAddressOfReadsBothCoordinateFields pins that the core resolves the WHOLE
// closed mgmt.address Facet (ADR-0084 {address, port?}) into the typed Target, not
// just the address. The port was declarable in the Facet schema but silently dropped
// here, so the schema advertised a knob no Run could honor (§1.8). Both fields cross
// the plugin port TYPED — the core never fuses them into "host:port" for a plugin to
// re-parse (§1.1), and never invents a default port (that is the tool's business).
func TestAddressOfReadsBothCoordinateFields(t *testing.T) {
	cases := map[string]struct {
		raw      string
		wantAddr string
		wantPort int32
	}{
		"address and port": {`{"address":"10.0.0.7","port":2222}`, "10.0.0.7", 2222},
		"address only":     {`{"address":"10.0.0.7"}`, "10.0.0.7", 0},
		"reserved local":   {`{"address":"local"}`, "local", 0},
		"empty raw":        {``, "", 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			addr, port := addressOf(json.RawMessage(tc.raw))
			if addr != tc.wantAddr || port != tc.wantPort {
				t.Fatalf("addressOf(%s) = (%q, %d), want (%q, %d)", tc.raw, addr, port, tc.wantAddr, tc.wantPort)
			}
		})
	}
	// The resolved coordinate reaches the Target typed and whole.
	addr, port := addressOf(json.RawMessage(`{"address":"10.0.0.7","port":2222}`))
	if got := renderTarget(types.Entity{ID: "e1"}, addr, port, nil); got.Address != "10.0.0.7" || got.Port != 2222 {
		t.Fatalf("renderTarget dropped part of the coordinate: %+v", got)
	}
	if got := renderTarget(types.Entity{ID: "e1"}, addr, port, nil); got.Transport != nil {
		t.Fatalf("no observed transport must stay nil, not an empty one — \"nothing observed\" and "+
			"\"observed to be nothing\" are different, and the second is not a state that exists: %+v", got.Transport)
	}
}

// The observed transport reaches the Target with its KIND legible and its coordinates OPAQUE
// (ADR-0156 D2). Core parses `kind` only, and only so a Run's descent can say which transport a
// target used — it never branches on it, which is what keeps the spine from holding a closed set
// of substrates it would have to grow (§9).
func TestTransportOf(t *testing.T) {
	raw := json.RawMessage(`{"kind":"kubectl","namespace":"stratt-hosts","pod":"web-01"}`)
	got := transportOf(raw)
	if got == nil || got.Kind != "kubectl" {
		t.Fatalf("kind must be legible: %+v", got)
	}
	if string(got.Coordinates) != string(raw) {
		t.Errorf("the coordinates must cross UNTOUCHED — their shape belongs to the transport, and "+
			"a core that parsed them would be learning what a Kubernetes namespace is: %s", got.Coordinates)
	}
	// A document with no kind yields NO transport rather than an empty one.
	for _, doc := range []string{``, `{}`, `{"namespace":"n"}`, `{not json`} {
		if tr := transportOf(json.RawMessage(doc)); tr != nil {
			t.Errorf("transportOf(%q) = %+v, want nil", doc, tr)
		}
	}
}
