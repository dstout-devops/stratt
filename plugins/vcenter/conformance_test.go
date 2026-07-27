package vcenter

import (
	"context"
	"testing"

	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	vimtypes "github.com/vmware/govmomi/vim25/types"

	"github.com/dstout-devops/stratt/sdk/mockstratt"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Port conformance for the vcenter Syncer, against the real simulator.
//
// WHY THIS EXISTS. ADR-0137 D2 calls the conformance suite "the thing a CI gate can
// run against a plugin it knows nothing about", and until now exactly one plugin in
// this repo ran it — an Actuator. The suite was Apply-shaped, so no Syncer could be
// checked at all; `ObserveConformance` (sdk/mockstratt) is the Observe half, and
// this is its first real subject.
//
// The specific defect it guards is the one that produced it. ADR-0143 made this
// plugin project `mgmt.address`, updated the Syncer's grant, and left the Manifest's
// `contracts` advertisement stale — so the plugin projected a Facet namespace it
// never advertised. That is invisible at runtime whenever the grant happens to be
// wide enough: the write simply lands under authority the operator was never asked
// for (§2.5). Three sets exist — advertised, granted, emitted — and this pairing was
// checked nowhere.
func TestObserveConformance(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		observed, err := enumerate(ctx, c)
		if err != nil {
			t.Fatalf("enumerate: %v", err)
		}
		if len(observed) == 0 {
			t.Fatal("the simulator produced no entities; this test would pass vacuously")
		}

		// The plugin's own advertisement, read the way the core reads it at
		// registration — never a list restated here, which would only assert that
		// this file and server.go were edited together.
		mres, err := (&Server{}).GetManifest(ctx, &pluginv1.GetManifestRequest{})
		if err != nil {
			t.Fatalf("GetManifest: %v", err)
		}

		entities := make([]mockstratt.Entity, 0, len(observed))
		for _, e := range observed {
			entities = append(entities, mockstratt.Entity{
				Kind:         e.GetKind(),
				IdentityKeys: e.GetIdentityKeys(),
				Labels:       e.GetLabels(),
				Facets:       e.GetFacets(),
			})
		}

		conf := mockstratt.ObserveConformance{
			// FullSyncComplete mirrors what Observe actually sends (server.go), so the
			// window-termination check judges the real behaviour rather than a default.
			Result:   mockstratt.ObserveResult{Entities: entities, FullSyncComplete: true},
			Manifest: mres.GetManifest(),
		}
		if errs := conf.Errors(); len(errs) > 0 {
			t.Fatalf("vcenter violates port conformance:\n%s", conf.Report())
		}
	})
}

// WHAT THE SIMULATOR SWEEP ABOVE DOES NOT COVER, measured rather than assumed:
// vcsim's VMs report no guest info, so `enumerate` projects 21 entities and ZERO of
// them carry `mgmt.address`. The sweep therefore passes VACUOUSLY for the one
// namespace it was written to guard — a test that is green for the wrong reason,
// which is the failure mode this whole session keeps turning up.
//
// So the namespace that matters gets a deterministic subject instead of a
// simulator-dependent one. normalizeVM with guest info is the real projection path;
// only the source of the input differs.
func TestObserveConformanceCoversTheReachCoordinate(t *testing.T) {
	ent := vmWithGuest(t)
	if len(ent.Facets["mgmt.address"]) == 0 {
		t.Fatal("fixture does not exercise mgmt.address; this test would be vacuous too")
	}
	mres, err := (&Server{}).GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	conf := mockstratt.ObserveConformance{
		Result:   mockstratt.ObserveResult{Entities: []mockstratt.Entity{ent}, FullSyncComplete: true},
		Manifest: mres.GetManifest(),
	}
	if errs := conf.Errors(); len(errs) > 0 {
		t.Fatalf("projecting a reach coordinate must be conformant:\n%s", conf.Report())
	}

	// And it must FAIL when the advertisement goes stale — the exact slip ADR-0143
	// shipped, which the simulator sweep could not have caught.
	m := mres.GetManifest()
	kept := m.GetContracts()[:0:0]
	for _, cd := range m.GetContracts() {
		if cd.GetSchemaId() != "mgmt.address" {
			kept = append(kept, cd)
		}
	}
	if len(kept) == len(m.GetContracts()) {
		t.Fatal("mgmt.address is not advertised; ADR-0143's Manifest entry has been lost")
	}
	m.Contracts = kept
	stale := mockstratt.ObserveConformance{
		Result:   mockstratt.ObserveResult{Entities: []mockstratt.Entity{ent}, FullSyncComplete: true},
		Manifest: m,
	}
	var caught bool
	for _, v := range stale.Errors() {
		if v.Check == "declares-what-it-emits" && v.Detail == "mgmt.address" {
			caught = true
		}
	}
	if !caught {
		t.Fatalf("a stale advertisement for mgmt.address must be caught:\n%s", stale.Report())
	}
}

// vmWithGuest runs the real normalizeVM over a VM that reports guest info, which is
// what vcsim does not give us.
func vmWithGuest(t *testing.T) mockstratt.Entity {
	t.Helper()
	e, err := normalizeVM(guestVM())
	if err != nil {
		t.Fatalf("normalizeVM: %v", err)
	}
	return mockstratt.Entity{
		Kind: e.GetKind(), IdentityKeys: e.GetIdentityKeys(),
		Labels: e.GetLabels(), Facets: e.GetFacets(),
	}
}

// The guard has to be able to FAIL, or it is decoration. Removing a namespace from
// the advertisement must be caught — this is the mutation check for the test above.
func TestObserveConformanceCatchesAStaleAdvertisement(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		observed, err := enumerate(ctx, c)
		if err != nil {
			t.Fatalf("enumerate: %v", err)
		}
		mres, err := (&Server{}).GetManifest(ctx, &pluginv1.GetManifestRequest{})
		if err != nil {
			t.Fatalf("GetManifest: %v", err)
		}
		m := mres.GetManifest()

		// Drop vm.config from the advertisement while the plugin keeps projecting it —
		// exactly the shape ADR-0143 shipped, simulated.
		kept := m.GetContracts()[:0:0]
		for _, cd := range m.GetContracts() {
			if cd.GetSchemaId() != "vm.config" {
				kept = append(kept, cd)
			}
		}
		if len(kept) == len(m.GetContracts()) {
			t.Fatal("vm.config is no longer advertised; this mutation check needs updating")
		}
		m.Contracts = kept

		entities := make([]mockstratt.Entity, 0, len(observed))
		for _, e := range observed {
			entities = append(entities, mockstratt.Entity{Kind: e.GetKind(), Facets: e.GetFacets()})
		}
		conf := mockstratt.ObserveConformance{
			Result:   mockstratt.ObserveResult{Entities: entities, FullSyncComplete: true},
			Manifest: m,
		}
		var caught bool
		for _, v := range conf.Errors() {
			if v.Check == "declares-what-it-emits" && v.Detail == "vm.config" {
				caught = true
			}
		}
		if !caught {
			t.Fatalf("an unadvertised namespace must be caught:\n%s", conf.Report())
		}
	})
}

// guestVM is a VM that reports guest info — the state vcsim never reaches. The
// hostname is dotted so the reach coordinate resolves to a NAME (ADR-0143 D1).
func guestVM() mo.VirtualMachine {
	return mo.VirtualMachine{
		Config: &vimtypes.VirtualMachineConfigInfo{
			Uuid:     "42246c1a-0000-0000-0000-conformance",
			Hardware: vimtypes.VirtualHardware{NumCPU: 2, MemoryMB: 4096},
		},
		Guest: &vimtypes.GuestInfo{
			HostName:  "web-01.dev.stratt.test",
			IpAddress: "10.30.1.7",
		},
	}
}
