package vcenter

import (
	"context"
	"testing"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	vimtypes "github.com/vmware/govmomi/vim25/types"
)

// TestSnapshotRoundTrip: create → revert → remove a snapshot, all against in-process vcsim.
func TestSnapshotRoundTrip(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		uuid := mkVM(ctx, t, c, "snap-01", 1, 512)
		if err := snapshotCreate(ctx, c, uuid, "s1", "first", false, false); err != nil {
			t.Fatalf("snapshotCreate: %v", err)
		}
		if err := snapshotRevert(ctx, c, uuid, "s1"); err != nil {
			t.Fatalf("snapshotRevert: %v", err)
		}
		if err := snapshotRemove(ctx, c, uuid, "s1", false); err != nil {
			t.Fatalf("snapshotRemove: %v", err)
		}
		if err := snapshotCreate(ctx, c, "no-such-uuid", "s", "", false, false); err != errVMNotFound {
			t.Errorf("snapshot on a missing uuid must return errVMNotFound, got %v", err)
		}
	})
}

// TestCloneVM: clone creates a NEW VM with a fresh vcenter.uuid, and the Syncer observes both.
func TestCloneVM(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		src := mkVM(ctx, t, c, "clone-src", 1, 512)
		newUUID, err := cloneVM(ctx, c, src, "clone-dst")
		if err != nil {
			t.Fatalf("cloneVM: %v", err)
		}
		if newUUID == "" || newUUID == src {
			t.Fatalf("clone must yield a NEW uuid, got %q (src %q)", newUUID, src)
		}
		if !vmObserved(ctx, t, c, src) || !vmObserved(ctx, t, c, newUUID) {
			t.Errorf("Syncer must observe both source and clone (src=%v dst=%v)",
				vmObserved(ctx, t, c, src), vmObserved(ctx, t, c, newUUID))
		}
	})
}

// TestMigrateVM: relocate a VM to another host in the datacenter.
func TestMigrateVM(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		uuid := mkVM(ctx, t, c, "mig-01", 1, 512)
		finder := find.NewFinder(c, true)
		dc, _ := finder.DefaultDatacenter(ctx)
		finder.SetDatacenter(dc)
		hosts, err := finder.HostSystemList(ctx, "*")
		if err != nil || len(hosts) == 0 {
			t.Skipf("no hosts to migrate to: %v", err)
		}
		if err := migrateVM(ctx, c, uuid, hosts[len(hosts)-1].Name(), ""); err != nil {
			t.Fatalf("migrateVM: %v", err)
		}
	})
}

// TestPortgroupReconfigureAndDelete: create a VLAN portgroup, change its VLAN, then delete it — and the
// delete is idempotent on absence (ADR-0114 D2).
func TestPortgroupReconfigureAndDelete(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		res, err := provisionPortgroup(ctx, c, createPortgroupParams{Name: "lc-pg", VLANID: 100})
		if err != nil {
			t.Fatalf("provisionPortgroup: %v", err)
		}
		if err := reconfigurePortgroup(ctx, c, res.Moref, 250); err != nil {
			t.Fatalf("reconfigurePortgroup: %v", err)
		}
		// Assert the new VLAN via the object config.
		ref := vimtypes.ManagedObjectReference{Type: pgObjType, Value: res.Moref}
		pg := object.NewDistributedVirtualPortgroup(c, ref)
		var mpg mo.DistributedVirtualPortgroup
		if err := pg.Properties(ctx, ref, []string{"config"}, &mpg); err != nil {
			t.Fatalf("props: %v", err)
		}
		got := -1
		if s, ok := mpg.Config.DefaultPortConfig.(*vimtypes.VMwareDVSPortSetting); ok {
			if v, ok := s.Vlan.(*vimtypes.VmwareDistributedVirtualSwitchVlanIdSpec); ok {
				got = int(v.VlanId)
			}
		}
		if got != 250 {
			t.Errorf("reconfigure-portgroup VLAN not applied: got %d want 250", got)
		}
		found, err := deletePortgroup(ctx, c, res.Moref)
		if err != nil || !found {
			t.Fatalf("deletePortgroup: found=%v err=%v", found, err)
		}
		// Gone now → resolve returns nil, delete is an idempotent no-op.
		pgh, _, err := resolvePortgroup(ctx, c, res.Moref)
		if err != nil {
			t.Fatalf("resolve after delete: %v", err)
		}
		if pgh != nil {
			t.Errorf("portgroup should be gone after delete")
		}
		if again, err := deletePortgroup(ctx, c, res.Moref); err != nil || again {
			t.Errorf("re-delete must be idempotent no-op: found=%v err=%v", again, err)
		}
	})
}
