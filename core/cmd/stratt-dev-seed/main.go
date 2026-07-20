// Command stratt-dev-seed projects one synthetic Entity into the graph so the
// plugin e2e's `dev-hosts` View has a deterministic target for the EE-Job
// Actuator Runs (a `dev-host` kind, isolated from the vcenter plugin's real
// `host`/`vm` Entities). It writes through the ONLY legal Normalizer seam
// (§1.2): RegisterSource → NormalizerProjector().UpsertEntities with WriterSyncer
// provenance. No secrets touch the graph (§2.5). Idempotent — the Source upserts
// on name and the Entity correlates on its identity key.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

func main() {
	dsn := os.Getenv("STRATT_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://stratt:stratt-dev@localhost:5432/stratt"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := graph.Connect(ctx, dsn)
	if err != nil {
		log.Fatalf("stratt-dev-seed: connect %s: %v", dsn, err)
	}
	defer store.Close()

	src, err := store.RegisterSource(ctx, types.Source{
		Kind:     "dev-seed",
		Name:     "dev-seed",
		Endpoint: "seed://dev",
	})
	if err != nil {
		log.Fatalf("stratt-dev-seed: register source: %v", err)
	}

	// ADR-0084: reachability is the typed mgmt.address Facet — the silent
	// ansible_connection:local default is retired. This synthetic host runs on the
	// EE pod itself, so it declares `local` EXPLICITLY (an opt-in, not a fallback),
	// registering itself as an owner of the namespace first (§2.1 / ADR-0060).
	if err := store.RegisterFacetOwner(ctx, types.FacetOwner{
		Namespace: "mgmt.address", OwnerKind: "syncer", OwnerRef: "dev-seed/syncer",
	}); err != nil {
		log.Fatalf("stratt-dev-seed: register mgmt.address owner: %v", err)
	}

	ids, err := store.NormalizerProjector().UpsertEntities(ctx, types.Provenance{
		WriterKind: types.WriterSyncer,
		WriterRef:  "dev-seed/syncer",
		SourceID:   src.ID,
		At:         time.Now().UTC(),
	}, []graph.EntityUpsert{{
		Kind:         "dev-host",
		IdentityKeys: map[string]string{"dns.fqdn": "dev-1"},
		Facets:       map[string]json.RawMessage{"mgmt.address": json.RawMessage(`{"address":"local"}`)},
	}})
	if err != nil {
		log.Fatalf("stratt-dev-seed: upsert entity: %v", err)
	}

	log.Printf("✓ seeded dev-host entity %s (source %s) — View dev-hosts now selects 1 target", ids[0], src.ID)
}
