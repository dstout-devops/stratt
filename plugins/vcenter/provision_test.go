package vcenter

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// TestProvisionVMCreatesAndObserves proves the read<->build loop (ADR-0113 D5) in isolation: the
// provisioning Action creates + powers on a VM via govmomi against the in-process vcsim, returns its
// stable vcenter.uuid identity, and the Syncer's OWN enumerate path then OBSERVEs that VM back at the
// same identity. One module owns both verbs, so the correlation is structural (ADR-0113 D1/D3).
func TestProvisionVMCreatesAndObserves(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		res, err := provisionVM(ctx, c, createVMParams{Name: "stratt-web-01", CPUs: 2, MemoryMB: 2048})
		if err != nil {
			t.Fatalf("provisionVM: %v", err)
		}
		if res.UUID == "" {
			t.Fatal("built VM has empty vcenter.uuid — Syncer could not correlate it")
		}

		entities, err := enumerate(ctx, c)
		if err != nil {
			t.Fatalf("enumerate: %v", err)
		}
		var observed bool
		for _, e := range entities {
			if e.GetKind() == "vm" && e.GetIdentityKeys()["vcenter.uuid"] == res.UUID {
				observed = true
			}
		}
		if !observed {
			t.Errorf("Syncer did not OBSERVE the built VM by vcenter.uuid=%s", res.UUID)
		}
	})
}

// TestBuildEntityIsIdentityOnly guards the ADR-0113 D3 / ADR-0112 D5 invariant: the build projection
// carries identity + labels only — NEVER a Facet (the Syncer owns vm.config/vm.runtime by OBSERVE, so
// the build must not become a second/fourth writer, §1.2). It also honors the estate overlay.
func TestBuildEntityIsIdentityOnly(t *testing.T) {
	e := buildEntity(createVMParams{
		Name:          "web-01",
		ProjectKind:   "host",
		ProjectLabels: map[string]string{"fleet": "web", "stratt.intent/instance": "web-01"},
	}, vmResult{UUID: "abc-123", Moref: "vm-42"})

	if len(e.GetFacets()) != 0 {
		t.Errorf("build output must write NO Facets (Syncer owns them), got %v", e.GetFacets())
	}
	if e.GetIdentityKeys()["vcenter.uuid"] != "abc-123" {
		t.Errorf("build output must key by vcenter.uuid, got %v", e.GetIdentityKeys())
	}
	if e.GetKind() != "host" {
		t.Errorf("projectKind overlay not applied: got kind %q", e.GetKind())
	}
	if e.GetLabels()["fleet"] != "web" || e.GetLabels()["stratt.intent/instance"] != "web-01" {
		t.Errorf("projectLabels overlay not applied: got %v", e.GetLabels())
	}
	if e.GetLabels()["source"] != "vsphere" {
		t.Errorf("build output must carry source=vsphere, got %q", e.GetLabels()["source"])
	}
}

// TestProvisionPortgroupCreatesWithVLAN proves the Subnet build (ADR-0113 D4): create-portgroup builds
// a DVS portgroup with the given 802.1Q VLAN against in-process vcsim, and the Syncer OBSERVEs it back
// as a subnet by vcenter.network.moref. The VLAN arrives as a plain param (bound from a prior
// netbox/ipam-resolve Step's outputs at the Workflow layer) — no resolve-inject.
func TestProvisionPortgroupCreatesWithVLAN(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		res, err := provisionPortgroup(ctx, c, createPortgroupParams{Name: "dmz-pg", VLANID: 1234})
		if err != nil {
			t.Fatalf("provisionPortgroup: %v", err)
		}
		if res.Moref == "" {
			t.Fatal("built portgroup has empty moref — Syncer could not correlate it")
		}
		entities, err := enumerate(ctx, c)
		if err != nil {
			t.Fatalf("enumerate: %v", err)
		}
		var observed bool
		for _, e := range entities {
			if e.GetKind() == "subnet" && e.GetIdentityKeys()["vcenter.network.moref"] == res.Moref {
				observed = true
			}
		}
		if !observed {
			t.Errorf("Syncer did not OBSERVE the built portgroup by vcenter.network.moref=%s", res.Moref)
		}
	})
}

// TestBuildPortgroupEntityIsIdentityOnly guards ADR-0113 D3 for the portgroup build: identity + labels
// only, keyed by vcenter.network.moref, NEVER a net.subnet Facet (the Syncer owns it).
func TestBuildPortgroupEntityIsIdentityOnly(t *testing.T) {
	e := buildPortgroupEntity(createPortgroupParams{Name: "dmz-pg", ProjectLabels: map[string]string{"zone": "dmz"}}, pgResult{Moref: "dvportgroup-77"})
	if len(e.GetFacets()) != 0 {
		t.Errorf("portgroup build must write NO Facets (Syncer owns net.subnet), got %v", e.GetFacets())
	}
	if e.GetIdentityKeys()["vcenter.network.moref"] != "dvportgroup-77" {
		t.Errorf("portgroup build must key by vcenter.network.moref, got %v", e.GetIdentityKeys())
	}
	if e.GetKind() != "subnet" {
		t.Errorf("default kind must be subnet, got %q", e.GetKind())
	}
	if e.GetLabels()["source"] != "vsphere" || e.GetLabels()["zone"] != "dmz" {
		t.Errorf("labels/overlay not applied: %v", e.GetLabels())
	}
}

// TestUnknownActionRejected: content-blind dispatch rejects an action the plugin does not ship (§1.5)
// rather than guessing.
func TestUnknownActionRejected(t *testing.T) {
	s := NewServer(Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// The reject path returns before touching the stream, so a nil stream is safe here.
	err := s.Invoke(&pluginv1.InvokeRequest{Action: "vcenter/nope"}, nil)
	if err == nil {
		t.Fatal("expected an error for an unknown action")
	}
}
