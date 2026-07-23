package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestConnectorStore proves the CaC-only Connector store CRUD + env-scope filter (ADR-0103).
func TestConnectorStore(t *testing.T) {
	s := testStore(t)
	s.SetEnvironment("dev") // so the env-scope filter is active (ADR-0057)
	ctx := context.Background()

	c := types.Connector{
		Name: "vcenter-dev", Class: types.ConnectorSyncer, Address: "vcenter.svc:9090",
		PluginIdentity: "vcenter", Tier: "trusted",
		Source:          types.Source{Kind: "vcenter", Name: "vcenter-dev", Endpoint: "https://vcsim/sdk"},
		FacetNamespaces: []string{"instance.compute"}, IdentitySchemes: []string{"vcenter.uuid"},
		IntervalSeconds: 15,
	}
	if err := s.UpsertConnector(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConnector(ctx, "vcenter-dev")
	if err != nil || got.Address != "vcenter.svc:9090" || got.Source.Kind != "vcenter" || got.Class != types.ConnectorSyncer {
		t.Fatalf("round-trip: %+v err=%v", got, err)
	}
	// Upsert is idempotent-update.
	c.IntervalSeconds = 30
	if err := s.UpsertConnector(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetConnector(ctx, "vcenter-dev"); got.IntervalSeconds != 30 {
		t.Fatalf("update must overwrite spec: %+v", got)
	}
	// Env scope: a prod-only connector is filtered out of the dev environment.
	if err := s.UpsertConnector(ctx, types.Connector{Name: "prod-only", Class: types.ConnectorSyncer, Address: "x:9090", PluginIdentity: "p", Source: types.Source{Kind: "p", Name: "prod-only"}, Environments: []string{"prod"}}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListConnectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, lc := range list {
		if lc.Name == "prod-only" {
			t.Fatal("a prod-only connector must be filtered out of the dev environment (ADR-0057)")
		}
	}
	// Delete.
	if err := s.DeleteConnector(ctx, "vcenter-dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetConnector(ctx, "vcenter-dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted connector must be ErrNotFound, got %v", err)
	}
	_ = s.DeleteConnector(ctx, "prod-only") // cleanup
}

// TestActuatorStore proves the CaC-only Actuator store CRUD (ADR-0103) — no Source.
func TestActuatorStore(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	a := types.Actuator{
		Name: "helm", Address: "stratt-helm.svc:9090", PluginIdentity: "helm", Tier: "trusted",
		DryRunnable: true, ActionNames: []string{"helm/deploy"},
	}
	if err := s.UpsertActuator(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetActuator(ctx, "helm")
	if err != nil || got.Address != "stratt-helm.svc:9090" || len(got.ActionNames) != 1 || got.ActionNames[0] != "helm/deploy" {
		t.Fatalf("round-trip: %+v err=%v", got, err)
	}
	if !got.DryRunnable {
		t.Fatal("actuator dryRunnable must round-trip")
	}
	if err := s.DeleteActuator(ctx, "helm"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetActuator(ctx, "helm"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted actuator must be ErrNotFound, got %v", err)
	}
}
