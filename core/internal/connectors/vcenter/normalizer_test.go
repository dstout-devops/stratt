package vcenter

import (
	"context"
	"testing"

	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
)

// TestNormalizeAgainstSimulator retrieves VMs and hosts from an in-process
// vcsim instance with the exact property sets the Syncer uses, and checks the
// Normalizer produces well-formed projections: identity present, kind right,
// this Connector's Facet namespaces populated.
func TestNormalizeAgainstSimulator(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		m := view.NewManager(c)

		vv, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"VirtualMachine"}, true)
		if err != nil {
			t.Fatal(err)
		}
		var vms []mo.VirtualMachine
		if err := vv.Retrieve(ctx, []string{"VirtualMachine"}, vmProps, &vms); err != nil {
			t.Fatal(err)
		}
		if len(vms) == 0 {
			t.Fatal("simulator returned no VMs")
		}
		for _, vm := range vms {
			up, err := normalizeVM(vm)
			if err != nil {
				t.Fatalf("normalize vm %q: %v", vm.Name, err)
			}
			if up.Kind != "vm" {
				t.Errorf("vm %q: kind = %q", vm.Name, up.Kind)
			}
			if up.IdentityKeys["vcenter.uuid"] == "" {
				t.Errorf("vm %q: missing vcenter.uuid identity", vm.Name)
			}
			for _, ns := range []string{"vm.config", "vm.runtime"} {
				if _, ok := up.Facets[ns]; !ok {
					t.Errorf("vm %q: facet %s missing", vm.Name, ns)
				}
			}
		}

		hv, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"HostSystem"}, true)
		if err != nil {
			t.Fatal(err)
		}
		var hosts []mo.HostSystem
		if err := hv.Retrieve(ctx, []string{"HostSystem"}, hostProps, &hosts); err != nil {
			t.Fatal(err)
		}
		if len(hosts) == 0 {
			t.Fatal("simulator returned no hosts")
		}
		for _, h := range hosts {
			up, err := normalizeHost(h)
			if err != nil {
				t.Fatalf("normalize host %q: %v", h.Name, err)
			}
			if up.IdentityKeys["vcenter.host.uuid"] == "" {
				t.Errorf("host %q: missing identity", h.Name)
			}
		}
		t.Logf("normalized %d VMs and %d hosts from simulator", len(vms), len(hosts))
	})
}
