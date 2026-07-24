// Enterprise-topology dev seed (ADR-0113 follow-up #3): shape a running vcsim into a multi-REGION /
// availability-ZONE / SOVEREIGNTY-tenant / VLAN estate, so Stratt's vSphere read+build story is
// demonstrable against realistic enterprise scenarios rather than vcsim's flat default. It is dev-only
// (vcsim is not a real vCenter); it uses the SAME govmomi content-expertise the plugin already ships.
//
// Idempotent: every object is created only if absent (safe to re-run after each `task dev:up`, like
// netbox-bootstrap.sh). Pure over a vim25 client, so it is tested against the in-process simulator.
package vcenter

import (
	"context"
	"fmt"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25"
	vimtypes "github.com/vmware/govmomi/vim25/types"
)

// Connect dials vCenter/vcsim for the dev seed (and any out-of-band tooling) — an exported wrapper
// over the plugin's internal connect, so the seed reuses the exact same connection path the Syncer uses.
func Connect(ctx context.Context, cfg Config) (*govmomi.Client, error) { return connect(ctx, cfg) }

// Region models one enterprise region as a vSphere datacenter, its availability zones as clusters, its
// sovereignty tenants as VM folders, and its network segments as VLAN-tagged DVS portgroups.
type Region struct {
	Name              string
	AvailabilityZones []string  // clusters
	Tenants           []string  // sovereignty VM folders
	Segments          []Segment // VLAN portgroups on the region's DVS
}

// Segment is one network segment: a DVS portgroup carved at a specific 802.1Q VLAN.
type Segment struct {
	Name string
	VLAN int32
}

// DefaultTopology is the enterprise estate the dev seed lays down: two regions (a standard region and a
// sovereign region), each with two AZs, two tenants, and four VLAN segments — enough to exercise
// region/AZ/sovereignty/VLAN placement end to end.
func DefaultTopology() []Region {
	segs := []Segment{
		{Name: "web", VLAN: 100},
		{Name: "app", VLAN: 200},
		{Name: "db", VLAN: 300},
		{Name: "dmz", VLAN: 400},
	}
	return []Region{
		{
			Name:              "region-us-east",
			AvailabilityZones: []string{"az-use1-a", "az-use1-b"},
			Tenants:           []string{"tenant-acme", "tenant-globex"},
			Segments:          segs,
		},
		{
			Name:              "region-eu-sovereign",
			AvailabilityZones: []string{"az-eus1-a", "az-eus1-b"},
			Tenants:           []string{"tenant-sovereign-gov"},
			Segments:          segs,
		},
	}
}

// Seed applies the topology idempotently against a vim25 client. It returns the count of objects it
// CREATED (0 on a clean re-run), so the caller can report "already seeded" vs "seeded N objects".
func Seed(ctx context.Context, c *vim25.Client, regions []Region) (int, error) {
	created := 0
	rootFolder := object.NewFolder(c, c.ServiceContent.RootFolder)
	for _, r := range regions {
		finder := find.NewFinder(c, true)
		dc, err := finder.Datacenter(ctx, r.Name)
		if err != nil {
			d, cerr := rootFolder.CreateDatacenter(ctx, r.Name)
			if cerr != nil {
				return created, fmt.Errorf("create datacenter %q: %w", r.Name, cerr)
			}
			created++
			dc = d
		}
		finder.SetDatacenter(dc)
		folders, err := dc.Folders(ctx)
		if err != nil {
			return created, fmt.Errorf("datacenter %q folders: %w", r.Name, err)
		}

		// Availability zones → clusters under the host folder.
		for _, az := range r.AvailabilityZones {
			if _, err := finder.ClusterComputeResource(ctx, az); err != nil {
				if _, cerr := folders.HostFolder.CreateCluster(ctx, az, vimtypes.ClusterConfigSpecEx{}); cerr != nil {
					return created, fmt.Errorf("create cluster %q in %q: %w", az, r.Name, cerr)
				}
				created++
			}
		}

		// Sovereignty tenants → VM folders (absolute inventory path: /<dc>/vm/<tenant>).
		for _, t := range r.Tenants {
			if _, err := finder.Folder(ctx, "/"+r.Name+"/vm/"+t); err != nil {
				if _, cerr := folders.VmFolder.CreateFolder(ctx, t); cerr != nil {
					return created, fmt.Errorf("create tenant folder %q in %q: %w", t, r.Name, cerr)
				}
				created++
			}
		}

		// One DVS per region carrying the VLAN segments.
		dvsName := "dvs-" + r.Name
		dvs, err := findRegionDVS(ctx, finder, dvsName)
		if err != nil {
			d, cerr := createDVS(ctx, folders.NetworkFolder, dvsName)
			if cerr != nil {
				return created, fmt.Errorf("create dvs %q in %q: %w", dvsName, r.Name, cerr)
			}
			created++
			dvs = d
		}
		for _, s := range r.Segments {
			pgName := fmt.Sprintf("%s-%s-vlan%d", r.Name, s.Name, s.VLAN)
			if _, err := finder.Network(ctx, pgName); err == nil {
				continue // already present
			}
			if err := addSegment(ctx, dvs, pgName, s.VLAN); err != nil {
				return created, fmt.Errorf("add portgroup %q: %w", pgName, err)
			}
			created++
		}
	}
	return created, nil
}

// findRegionDVS resolves a region's DVS by name, or returns an error if absent (so Seed creates it).
func findRegionDVS(ctx context.Context, finder *find.Finder, name string) (*object.DistributedVirtualSwitch, error) {
	n, err := finder.Network(ctx, name)
	if err != nil {
		return nil, err
	}
	dvs, ok := n.(*object.DistributedVirtualSwitch)
	if !ok {
		return nil, fmt.Errorf("%q is not a distributed virtual switch (%T)", name, n)
	}
	return dvs, nil
}

// createDVS creates a distributed virtual switch in the network folder and returns it.
func createDVS(ctx context.Context, netFolder *object.Folder, name string) (*object.DistributedVirtualSwitch, error) {
	spec := vimtypes.DVSCreateSpec{
		ConfigSpec: &vimtypes.VMwareDVSConfigSpec{
			DVSConfigSpec: vimtypes.DVSConfigSpec{Name: name},
		},
	}
	task, err := netFolder.CreateDVS(ctx, spec)
	if err != nil {
		return nil, err
	}
	info, err := task.WaitForResult(ctx, nil)
	if err != nil {
		return nil, err
	}
	ref, ok := info.Result.(vimtypes.ManagedObjectReference)
	if !ok {
		return nil, fmt.Errorf("create dvs: unexpected task result %T", info.Result)
	}
	return object.NewDistributedVirtualSwitch(netFolder.Client(), ref), nil
}

// addSegment adds one VLAN-tagged portgroup to a DVS.
func addSegment(ctx context.Context, dvs *object.DistributedVirtualSwitch, name string, vlan int32) error {
	spec := vimtypes.DVPortgroupConfigSpec{
		Name:     name,
		Type:     string(vimtypes.DistributedVirtualPortgroupPortgroupTypeEarlyBinding),
		NumPorts: 8,
		DefaultPortConfig: &vimtypes.VMwareDVSPortSetting{
			Vlan: &vimtypes.VmwareDistributedVirtualSwitchVlanIdSpec{VlanId: vlan},
		},
	}
	task, err := dvs.AddPortgroup(ctx, []vimtypes.DVPortgroupConfigSpec{spec})
	if err != nil {
		return err
	}
	_, err = task.WaitForResult(ctx, nil)
	return err
}
