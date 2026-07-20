package salt

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/dstout-devops/stratt/plugins/salt/saltsim"
)

// TestPackageCollection drives the real ADR-0080 slice-2b collector path against
// saltsim: enumerate (grains → host) then the opt-in pkg.list_pkgs → software.package
// projection, asserting the salt-api → Facet mapping the wire carries.
func TestPackageCollection(t *testing.T) {
	sim := saltsim.New()
	sim.SetMinion("web-01.acme.internal", minionGrains("web-01.acme.internal", "web-01.acme.internal", "Debian"))
	sim.SetMinion("db-01.acme.internal", minionGrains("db-01.acme.internal", "db-01.acme.internal", "Debian"))
	// web-01 has packages; db-01 reports none — it must project no software.package.
	sim.SetPackages("web-01.acme.internal", map[string]string{"openssl": "3.0.5", "curl": "8.5.0"})
	srv := httptest.NewServer(sim.Handler())
	defer srv.Close()

	ctx := context.Background()
	client, err := connect(ctx, Config{APIURL: srv.URL, Username: "stratt", Password: "pw", CollectPackages: true})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	entities, err := enumerate(ctx, client)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	// The exact collector step Observe runs when CollectPackages is on.
	pkgs, err := client.listPkgs(ctx)
	if err != nil {
		t.Fatalf("listPkgs: %v", err)
	}
	attachPackages(entities, pkgs)

	web := findEntity(t, entities, "web-01.acme.internal")
	raw := web.GetFacets()["software.package"]
	if len(raw) == 0 {
		t.Fatal("web-01 must carry a software.package facet")
	}
	var inv struct {
		Packages []struct{ Name, Version, Origin, DeliveryForm string }
	}
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatalf("software.package: %v", err)
	}
	if len(inv.Packages) != 2 {
		t.Fatalf("want 2 packages, got %d", len(inv.Packages))
	}
	// Sorted by name → curl, openssl; carries version + the open form attributes.
	if inv.Packages[0].Name != "curl" || inv.Packages[1].Name != "openssl" {
		t.Fatalf("packages not sorted deterministically: %+v", inv.Packages)
	}
	if inv.Packages[1].Version != "3.0.5" || inv.Packages[1].DeliveryForm != "package" || inv.Packages[1].Origin != "distro" {
		t.Fatalf("openssl package shape wrong: %+v", inv.Packages[1])
	}

	// db-01 reported no packages → no software.package facet.
	db := findEntity(t, entities, "db-01.acme.internal")
	if len(db.GetFacets()["software.package"]) != 0 {
		t.Fatal("db-01 reported no packages; it must not carry a software.package facet")
	}
}

// TestManifestPackageOwnership: salt claims the shared software.package namespace
// (§2.1 single owner) ONLY when the collector is enabled.
func TestManifestPackageOwnership(t *testing.T) {
	has := func(cfg Config) bool {
		resp, err := NewServer(cfg, slogDiscard()).GetManifest(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range resp.GetManifest().GetContracts() {
			if c.GetSchemaId() == "software.package" {
				return true
			}
		}
		return false
	}
	if has(Config{}) {
		t.Error("salt must NOT claim software.package when not collecting packages")
	}
	if !has(Config{CollectPackages: true}) {
		t.Error("salt must claim software.package (§2.1 owner) when collecting packages")
	}
}
