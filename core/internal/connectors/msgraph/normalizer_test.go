package msgraph

import (
	"encoding/json"
	"testing"
)

func TestNormalizeDevice(t *testing.T) {
	enabled := true
	up, err := normalizeDevice(device{
		ID: "obj-1", DeviceID: "dev-guid-1", DisplayName: "LAPTOP-01",
		OperatingSystem: "Windows", OperatingSystemVersion: "10.0.26100",
		AccountEnabled: &enabled, TrustType: "AzureAd", ProfileType: "RegisteredDevice",
		ApproxLastSignIn: "2026-07-11T10:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if up.Kind != "device" || up.IdentityKeys["graph.id"] != "obj-1" || up.Labels["graph.name"] != "LAPTOP-01" {
		t.Fatalf("upsert: %+v", up)
	}
	var os map[string]any
	if err := json.Unmarshal(up.Facets["device.os"], &os); err != nil || os["operatingSystem"] != "Windows" {
		t.Fatalf("device.os facet: %s %v", up.Facets["device.os"], err)
	}
	var id map[string]any
	if err := json.Unmarshal(up.Facets["device.identity"], &id); err != nil || id["trustType"] != "AzureAd" {
		t.Fatalf("device.identity facet: %s %v", up.Facets["device.identity"], err)
	}
	var st map[string]any
	if err := json.Unmarshal(up.Facets["device.state"], &st); err != nil || st["accountEnabled"] != true {
		t.Fatalf("device.state facet: %s %v", up.Facets["device.state"], err)
	}

	// No identity → refuse to project (never guess identity, §1.2).
	if _, err := normalizeDevice(device{DisplayName: "ghost"}); err == nil {
		t.Fatal("device without object id must be rejected")
	}

	// Sparse device → only populated facets appear.
	sparse, err := normalizeDevice(device{ID: "obj-2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sparse.Facets) != 0 {
		t.Fatalf("sparse device should carry no empty facets: %v", sparse.Facets)
	}
}
