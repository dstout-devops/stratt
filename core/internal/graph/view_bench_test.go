package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// seedEstate projects n Entities in batches: 10% prod, half linux/half
// windows kernels, one relation-bearing facet each — enough shape for the
// selector paths the gate exercises.
func seedEstate(tb testing.TB, s *Store, n int) {
	tb.Helper()
	ctx := context.Background()
	p := s.NormalizerProjector()
	pv := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "vcenter/syncer", SourceID: testSourceID, At: time.Now().UTC()}

	if err := s.RegisterFacetOwner(ctx, types.FacetOwner{Namespace: "os.kernel", OwnerKind: "syncer", OwnerRef: "vcenter/syncer"}); err != nil {
		tb.Fatal(err)
	}
	for _, k := range []string{"env", "ring"} {
		if err := s.RegisterLabelOwner(ctx, types.LabelOwner{Key: k, OwnerKind: "syncer", OwnerRef: "vcenter/syncer"}); err != nil {
			tb.Fatal(err)
		}
	}
	const batchSize = 1000
	for base := 0; base < n; base += batchSize {
		var batch []EntityUpsert
		for i := base; i < base+batchSize && i < n; i++ {
			env := "dev"
			if i%10 == 0 {
				env = "prod"
			}
			// linux on even indices so the gate selector (prod ∧ linux)
			// matches a real result set: prod is i%10==0 ⊂ even → 10% of
			// the estate. A zero-row query would understate the gate.
			family := "windows"
			if i%2 == 0 {
				family = "linux"
			}
			batch = append(batch, EntityUpsert{
				Kind:         "vm",
				IdentityKeys: map[string]string{"vcenter.uuid": fmt.Sprintf("u-%d", i)},
				Labels:       map[string]string{"env": env, "ring": fmt.Sprintf("r%d", i%5)},
				Facets: map[string]json.RawMessage{
					"os.kernel": json.RawMessage(fmt.Sprintf(`{"family":%q,"release":"6.%d"}`, family, i%20)),
				},
			})
		}
		if _, err := p.UpsertEntities(ctx, pv, batch); err != nil {
			tb.Fatal(err)
		}
	}
}

// TestViewQueryGate is the charter §8 go/no-go measurement: a View query over
// a 50k-Entity estate must resolve in < 200 ms. Run with the dev substrate up:
//
//	go test ./internal/graph/ -run TestViewQueryGate -v -timeout 20m
func TestViewQueryGate(t *testing.T) {
	if testing.Short() {
		t.Skip("gate measurement seeds 50k entities; skipped in -short")
	}
	s := testStore(t)
	ctx := context.Background()

	const estate = 50_000
	t.Logf("seeding %d entities…", estate)
	start := time.Now()
	seedEstate(t, s, estate)
	t.Logf("seeded in %s", time.Since(start))

	sel := types.ViewSelector{
		Kinds:  []string{"vm"},
		Labels: map[string]string{"env": "prod"},
		Facets: []types.FacetPredicate{{Namespace: "os.kernel", Path: "family", Equals: json.RawMessage(`"linux"`)}},
	}

	// Warm once (plan cache, buffers), then measure.
	if _, err := s.ResolveSelector(ctx, sel, nil, 0); err != nil {
		t.Fatal(err)
	}
	const rounds = 10
	var worst, total time.Duration
	var matched int
	for range rounds {
		t0 := time.Now()
		ents, err := s.ResolveSelector(ctx, sel, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		d := time.Since(t0)
		total += d
		if d > worst {
			worst = d
		}
		matched = len(ents)
	}
	avg := total / rounds
	t.Logf("view query over %d entities: matched=%d avg=%s worst=%s (gate: <200ms)", estate, matched, avg, worst)
	if worst >= 200*time.Millisecond {
		t.Errorf("charter §8 go/no-go gate FAILED: worst view query %s >= 200ms", worst)
	}
}
