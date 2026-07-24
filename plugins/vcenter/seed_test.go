package vcenter

import (
	"context"
	"testing"

	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25"
)

// TestSeedCreatesEnterpriseTopologyIdempotently proves the dev seed (ADR-0113 follow-up #3): the first
// Seed lays down the region/AZ/tenant/VLAN objects; a second Seed creates NOTHING (idempotent); and the
// Syncer's enumerate then OBSERVEs at least the seeded VLAN portgroups as subnets. In-process vcsim.
func TestSeedCreatesEnterpriseTopologyIdempotently(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		topo := DefaultTopology()

		seededSegments := 0
		for _, r := range topo {
			seededSegments += len(r.Segments)
		}

		before := countSubnets(ctx, t, c)

		created, err := Seed(ctx, c, topo)
		if err != nil {
			t.Fatalf("first Seed: %v", err)
		}
		if created == 0 {
			t.Fatal("first Seed created nothing — expected the full enterprise topology")
		}

		// Idempotent: a second Seed is a clean no-op.
		again, err := Seed(ctx, c, topo)
		if err != nil {
			t.Fatalf("second Seed: %v", err)
		}
		if again != 0 {
			t.Errorf("second Seed must be idempotent (0 created), got %d", again)
		}

		// The Syncer OBSERVEs the seeded VLAN portgroups as subnets (across the new datacenters).
		after := countSubnets(ctx, t, c)
		if got := after - before; got < seededSegments {
			t.Errorf("expected the Syncer to OBSERVE >= %d new subnets from the seed, got %d (before=%d after=%d)",
				seededSegments, got, before, after)
		}
		t.Logf("seed created %d objects; observed subnets %d -> %d", created, before, after)
	})
}

func countSubnets(ctx context.Context, t *testing.T, c *vim25.Client) int {
	t.Helper()
	entities, err := enumerate(ctx, c)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	n := 0
	for _, e := range entities {
		if e.GetKind() == "subnet" {
			n++
		}
	}
	return n
}
