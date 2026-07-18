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
		var vms, hosts, subnets int
		for _, e := range entities {
			switch e.GetKind() {
			case "vm":
				vms++
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
				hosts++
				if e.GetIdentityKeys()["vcenter.host.uuid"] == "" {
					t.Errorf("host missing vcenter.host.uuid identity")
				}
			case "subnet":
				subnets++
				if e.GetIdentityKeys()["vcenter.network.moref"] == "" {
					t.Errorf("subnet missing vcenter.network.moref identity")
				}
				if len(e.GetFacets()["net.subnet"]) == 0 {
					t.Errorf("subnet missing net.subnet facet blob")
				}
				if e.GetLabels()["source"] != "vsphere" {
					t.Errorf("vSphere subnet must carry source=vsphere, got %q", e.GetLabels()["source"])
				}
			default:
				t.Errorf("unexpected kind %q", e.GetKind())
			}
		}
		if vms == 0 || hosts == 0 || subnets == 0 {
			t.Fatalf("expected vms, hosts, and subnets from simulator, got %d vms / %d hosts / %d subnets", vms, hosts, subnets)
		}
		t.Logf("enumerated %d vms, %d hosts, %d subnets (vSphere networks)", vms, hosts, subnets)
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
