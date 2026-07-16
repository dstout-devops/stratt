package orchestrate

import (
	"fmt"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
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
