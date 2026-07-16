package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dstout-devops/stratt/core/internal/cellrouter"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// TestCellsE2E is the two-Cell end-to-end harness (ADR-0044). Slices 3–7 are each
// unit-tested in isolation (federation with fake peers, re-home store methods on
// one DB, the workflow under the Temporal test suite); this proves them TOGETHER:
// two REAL strattd API servers, each backed by its OWN Postgres, wired as peers
// over real HTTP with the real HMAC-signed cellrouter. It exercises the federated
// read + partial-result honesty (slice 3), and the fenced cross-Cell Source
// re-home over the real adopt HTTP path (slice 7), end to end. DB-gated: skips
// when no Postgres is reachable, runs in CI where `task dev:up` is live.
//
// What still needs a real fleet (not this harness): the measured 99.99% SLO and
// the full RunAcrossCells Job execution (Temporal + K8s) — see the cell-failover
// drill runbook. This proves the cross-Cell HTTP + DB mechanisms are correct.
func TestCellsE2E(t *testing.T) {
	ctx := context.Background()
	secret := []byte("e2e-shared-cell-secret")

	storeEU := e2eStore(t, "eu")
	storeUS := e2eStore(t, "us")

	srvEU := e2eServer(t, storeEU, "eu", secret)
	defer srvEU.Close()
	srvUS := e2eServer(t, storeUS, "us", secret)
	usClosed := false
	defer func() {
		if !usClosed {
			srvUS.Close()
		}
	}()

	// Cross-declare peers now that the httptest URLs exist. PeerCells reads
	// graph.cell per-request, so seeding after boot is fine.
	mustNil(t, storeEU.UpsertCell(ctx, types.Cell{Name: "us", Region: "test", Endpoint: srvUS.URL}))
	mustNil(t, storeUS.UpsertCell(ctx, types.Cell{Name: "eu", Region: "test", Endpoint: srvEU.URL}))

	// ── Slice 3: federated read unions both Cells' data ──────────────────────
	t.Run("federated read unions both cells", func(t *testing.T) {
		mustNil(t, storeEU.WriteOrphanFinding(ctx, "b-eu", "entity:eu-1", "warning", []byte(`{}`)))
		mustNil(t, storeUS.WriteOrphanFinding(ctx, "b-us", "entity:us-1", "critical", []byte(`{}`)))

		status, hdr, body := e2eGET(t, srvEU.URL+"/api/v1/findings", "admin")
		if status != http.StatusOK {
			t.Fatalf("federated read must be 200 when both Cells are up, got %d (unreachable=%q)", status, hdr.Get("X-Stratt-Cells-Unreachable"))
		}
		targets := findingTargets(t, body)
		if !targets["entity:eu-1"] || !targets["entity:us-1"] {
			t.Fatalf("federated /findings must union BOTH Cells; got targets %v", targets)
		}
		// The queried-Cells header names both (self + peer) — the honesty signal.
		if q := hdr.Get("X-Stratt-Cells-Queried"); q == "" {
			t.Fatalf("federated response must name the queried Cells")
		}
	})

	// ── Slice 7: fenced cross-Cell Source re-home over the REAL adopt path ────
	t.Run("fenced re-home eu to us", func(t *testing.T) {
		// A Source homed on EU with one projected Entity.
		src, err := storeEU.RegisterSource(ctx, types.Source{Kind: "vcenter", Name: "vc-eu", Endpoint: "https://vc.eu"})
		mustNil(t, err)
		ids, err := storeEU.NormalizerProjector().UpsertEntities(ctx,
			types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "vcenter/syncer", SourceID: src.ID, At: time.Now().UTC()},
			[]graph.EntityUpsert{{Kind: "vm", IdentityKeys: map[string]string{"vc.uuid": "u-eu-1"}}})
		mustNil(t, err)
		if len(ids) != 1 {
			t.Fatalf("seed entity: got %d ids", len(ids))
		}

		// SEAL on EU (the source Cell). The DB fence now rejects EU Normalizer
		// writes to this Source — the single-writer guarantee.
		sealed, err := storeEU.SealSourceForRehome(ctx, "vc-eu", "us")
		mustNil(t, err)

		// ADOPT on US over the REAL HMAC-signed, Principal-asserted PeerClient —
		// the exact path RehomeSourceWorkflow.ForwardAdopt takes in production.
		body, _ := json.Marshal(RehomeAdopt{
			Source: Source{Kind: src.Kind, Name: src.Name, Endpoint: src.Endpoint},
			Epoch:  sealed.HomeEpoch,
		})
		status, resp, err := cellrouter.NewPeerClient(secret).Post(ctx, srvUS.URL, "/sources/rehome-adopt", body, "admin", "human")
		mustNil(t, err)
		if status != http.StatusAccepted {
			t.Fatalf("cross-Cell adopt must be 202, got %d: %s", status, string(resp))
		}

		// US now homes the Source.
		gotUS, err := storeUS.GetSource(ctx, "vc-eu")
		mustNil(t, err)
		if gotUS.Cell != "us" || gotUS.RehomingTo != "" {
			t.Fatalf("US must home the adopted Source (cell=us, settled); got cell=%q rehomingTo=%q", gotUS.Cell, gotUS.RehomingTo)
		}

		// A spoofed adopt WITHOUT the HMAC fan-out (a plain POST) is refused —
		// the endpoint is peer-internal only.
		plain, _ := http.NewRequestWithContext(ctx, http.MethodPost, srvUS.URL+"/api/v1/sources/rehome-adopt", nil)
		plain.Header.Set("X-Stratt-Principal", "mallory")
		pr, _ := http.DefaultClient.Do(plain)
		if pr != nil && pr.StatusCode < 400 {
			t.Fatalf("un-signed adopt must be refused, got %d", pr.StatusCode)
		}

		// COMPLETE on EU: the re-homed Entity is tombstoned (not hard-deleted),
		// the Source row retired.
		n, err := storeEU.CompleteRehome(ctx, "vc-eu")
		mustNil(t, err)
		if n != 1 {
			t.Fatalf("complete must tombstone the 1 re-homed Entity, got %d", n)
		}
		if _, err := storeEU.GetSource(ctx, "vc-eu"); err == nil {
			t.Fatal("EU must no longer home the Source after complete")
		}
	})

	// ── Slice 3/§1.8: an unreachable peer is a loud 206, never a silent drop ──
	t.Run("peer down is a partial 206", func(t *testing.T) {
		srvUS.Close()
		usClosed = true

		status, hdr, body := e2eGET(t, srvEU.URL+"/api/v1/findings", "admin")
		if status != http.StatusPartialContent {
			t.Fatalf("an unreachable peer must yield 206 (partial), got %d", status)
		}
		if unreach := hdr.Get("X-Stratt-Cells-Unreachable"); unreach == "" {
			t.Fatal("the unreachable Cell must be NAMED in the response (§1.8), not silently dropped")
		}
		// EU's own data is still returned — partial, not empty.
		if !findingTargets(t, body)["entity:eu-1"] {
			t.Fatal("a partial read must still return the reachable Cell's data")
		}

		// A cross-Cell write to the downed peer fails LOUDLY (no silent success).
		st, _, err := cellrouter.NewPeerClient(secret).Post(ctx, srvUS.URL, "/sources/rehome-adopt", []byte(`{}`), "admin", "human")
		if err == nil && st < 400 {
			t.Fatalf("a write to an unreachable Cell must fail, got status %d err %v", st, err)
		}
	})
}

// ── harness helpers ──────────────────────────────────────────────────────────

type allowAllAuthz struct{}

func (allowAllAuthz) Check(context.Context, string, string, string) (bool, error) { return true, nil }
func (allowAllAuthz) CheckHealth(context.Context) error                           { return nil }

// e2eStore creates a fresh, migrated Postgres database for one Cell and returns
// its Store stamped with the Cell id. Skips the whole test when no Postgres is
// reachable (mirrors graph.testStore).
func e2eStore(t *testing.T, cell string) *graph.Store {
	t.Helper()
	url := os.Getenv("STRATT_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://stratt:stratt-dev@localhost:5432/stratt"
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}
	name := fmt.Sprintf("stratt_e2e_%s_%d", cell, time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create e2e database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close()
	})
	u, _ := neturl.Parse(url)
	u.Path = "/" + name
	st, err := graph.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect+migrate e2e database: %v", err)
	}
	t.Cleanup(st.Close)
	st.SetCell(cell)
	return st
}

// e2eServer wraps a real api.Server (backed by the given Store, wired as a peer)
// in an httptest server. Bus/Temporal are nil — the harness only drives federated
// reads and the re-home adopt path, none of which touch them.
func e2eServer(t *testing.T, store *graph.Store, cellID string, secret []byte) *httptest.Server {
	t.Helper()
	srv := &Server{
		Store:              store,
		Authz:              allowAllAuthz{},
		CellID:             cellID,
		CellSecret:         secret,
		Peers:              cellrouter.NewPeerClient(secret),
		DevPrincipalHeader: true,
		Log:                slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return httptest.NewServer(srv.Handler())
}

// e2eGET issues a GET as the given dev Principal and returns status, headers, body.
func e2eGET(t *testing.T, url, principal string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	mustNil(t, err)
	req.Header.Set("X-Stratt-Principal", principal)
	resp, err := http.DefaultClient.Do(req)
	mustNil(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, body
}

// findingTargets parses a /findings JSON array into a set of its target strings.
func findingTargets(t *testing.T, body []byte) map[string]bool {
	t.Helper()
	var findings []struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal(body, &findings); err != nil {
		t.Fatalf("decode findings (%s): %v", string(body), err)
	}
	out := map[string]bool{}
	for _, f := range findings {
		out[f.Target] = true
	}
	return out
}

func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
