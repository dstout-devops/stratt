package vcenter

import (
	"context"
	"testing"

	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25"
)

// TestEnumerateAgainstSimulator proves the plugin's content-expertise in
// isolation — vcsim in-process, no core, no Postgres. It asserts the govmomi→
// ObservedEntity mapping the wire carries: kind, identity, and this plugin's
// Facet blobs. (The host side of the wire is proven separately in core, so
// neither module imports the other — the module-isolation point of Phase B.)
func TestEnumerateAgainstSimulator(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		entities, err := enumerate(ctx, c)
		if err != nil {
			t.Fatalf("enumerate: %v", err)
		}
		count := map[string]int{}
		for _, e := range entities {
			count[e.GetKind()]++
			switch e.GetKind() {
			case "vm":
				if e.GetIdentityKeys()["vcenter.uuid"] == "" {
					t.Errorf("vm missing vcenter.uuid identity")
				}
				if len(e.GetFacets()["vm.config"]) == 0 {
					t.Errorf("vm missing vm.config facet blob")
				}
				if len(e.GetFacets()["vm.runtime"]) == 0 {
					t.Errorf("vm missing vm.runtime facet blob")
				}
			case "host":
				if e.GetIdentityKeys()["vcenter.host.uuid"] == "" {
					t.Errorf("host missing vcenter.host.uuid identity")
				}
			case "subnet":
				if e.GetIdentityKeys()["vcenter.network.moref"] == "" {
					t.Errorf("subnet missing vcenter.network.moref identity")
				}
				if len(e.GetFacets()["net.subnet"]) == 0 {
					t.Errorf("subnet missing net.subnet facet blob")
				}
				if e.GetLabels()["source"] != "vsphere" {
					t.Errorf("vSphere subnet must carry source=vsphere, got %q", e.GetLabels()["source"])
				}
			case "region": // datacenter (ADR-0115 D1)
				if e.GetIdentityKeys()["vcenter.datacenter.moref"] == "" {
					t.Errorf("region missing vcenter.datacenter.moref identity")
				}
				if len(e.GetFacets()) != 0 {
					t.Errorf("region must be a bare Entity (no Facet), got %v", e.GetFacets())
				}
			case "availability-zone": // cluster (ADR-0115 D1)
				if e.GetIdentityKeys()["vcenter.cluster.moref"] == "" {
					t.Errorf("availability-zone missing vcenter.cluster.moref identity")
				}
			default:
				t.Errorf("unexpected kind %q", e.GetKind())
			}
		}
		for _, k := range []string{"vm", "host", "subnet", "region", "availability-zone"} {
			if count[k] == 0 {
				t.Errorf("expected at least one %q from the simulator, got 0", k)
			}
		}
		t.Logf("enumerated %v", count)
	})
}

// TestEnumerateEmitsRunsOn proves the vcenter runs-on edge crosses the wire
// (ADR-0047 relations; the Phase-B regression restored): every simulator VM runs
// on a host, so at least one vm carries a runs-on edge targeting a host BY
// IDENTITY (vcenter.host.uuid), and that identity matches an emitted host.
func TestEnumerateEmitsRunsOn(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		entities, err := enumerate(ctx, c)
		if err != nil {
			t.Fatalf("enumerate: %v", err)
		}
		hostUUIDs := map[string]bool{}
		for _, e := range entities {
			if e.GetKind() == "host" {
				hostUUIDs[e.GetIdentityKeys()["vcenter.host.uuid"]] = true
			}
		}
		var edges int
		for _, e := range entities {
			if e.GetKind() != "vm" {
				continue
			}
			for _, r := range e.GetRelations() {
				if r.GetType() != "runs-on" {
					continue
				}
				edges++
				if r.GetToScheme() != "vcenter.host.uuid" {
					t.Errorf("runs-on must target by vcenter.host.uuid, got %q", r.GetToScheme())
				}
				if !hostUUIDs[r.GetToValue()] {
					t.Errorf("runs-on target %q is not an emitted host", r.GetToValue())
				}
			}
		}
		if edges == 0 {
			t.Fatal("expected at least one runs-on edge from a vm to its host")
		}
		t.Logf("emitted %d runs-on edges", edges)
	})
}

// TestEnumerateEmitsPlacedIn proves vSphere emits the placed-in edge (ADR-0059) from
// a VM to the network(s) it sits in, targeting the subnet BY IDENTITY
// (vcenter.network.moref) — the same edge shape a cloud Syncer uses, so a
// relation-aware View ("the VMs in network X") spans both managers.
func TestEnumerateEmitsPlacedIn(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		entities, err := enumerate(ctx, c)
		if err != nil {
			t.Fatalf("enumerate: %v", err)
		}
		subnetRefs := map[string]bool{}
		for _, e := range entities {
			if e.GetKind() == "subnet" {
				subnetRefs[e.GetIdentityKeys()["vcenter.network.moref"]] = true
			}
		}
		var edges int
		for _, e := range entities {
			if e.GetKind() != "vm" {
				continue
			}
			for _, r := range e.GetRelations() {
				if r.GetType() != "placed-in" {
					continue
				}
				edges++
				if r.GetToScheme() != "vcenter.network.moref" {
					t.Errorf("placed-in must target by vcenter.network.moref, got %q", r.GetToScheme())
				}
				if !subnetRefs[r.GetToValue()] {
					t.Errorf("placed-in target %q is not an emitted subnet", r.GetToValue())
				}
			}
		}
		if edges == 0 {
			t.Fatal("expected at least one placed-in edge from a vm to its network")
		}
		t.Logf("emitted %d placed-in edges (vm -> vSphere network)", edges)
	})
}

// TestEnumerateTopologyEdges proves the ADR-0115 D5 topology relations: availability-zone (cluster)
// --in-region--> region (datacenter) via the parent-walk, and host --member-of--> availability-zone.
// Both target BY IDENTITY and must resolve to an emitted Entity.
func TestEnumerateTopologyEdges(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		entities, err := enumerate(ctx, c)
		if err != nil {
			t.Fatalf("enumerate: %v", err)
		}
		regions := map[string]bool{}
		azs := map[string]bool{}
		for _, e := range entities {
			switch e.GetKind() {
			case "region":
				regions[e.GetIdentityKeys()["vcenter.datacenter.moref"]] = true
			case "availability-zone":
				azs[e.GetIdentityKeys()["vcenter.cluster.moref"]] = true
			}
		}
		var inRegion, memberOf int
		for _, e := range entities {
			for _, r := range e.GetRelations() {
				switch r.GetType() {
				case "in-region":
					inRegion++
					if r.GetToScheme() != "vcenter.datacenter.moref" {
						t.Errorf("in-region must target vcenter.datacenter.moref, got %q", r.GetToScheme())
					}
					if !regions[r.GetToValue()] {
						t.Errorf("in-region target %q is not an emitted region", r.GetToValue())
					}
				case "member-of":
					memberOf++
					if r.GetToScheme() != "vcenter.cluster.moref" {
						t.Errorf("member-of must target vcenter.cluster.moref, got %q", r.GetToScheme())
					}
					if !azs[r.GetToValue()] {
						t.Errorf("member-of target %q is not an emitted availability-zone", r.GetToValue())
					}
				}
			}
		}
		if inRegion == 0 {
			t.Error("expected at least one availability-zone --in-region--> region edge (the parent-walk)")
		}
		if memberOf == 0 {
			t.Error("expected at least one host --member-of--> availability-zone edge")
		}
		t.Logf("topology edges: in-region=%d member-of=%d", inRegion, memberOf)
	})
}
