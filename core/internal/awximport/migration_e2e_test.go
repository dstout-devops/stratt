package awximport

// migration_e2e_test.go is the AAP-replacement end-to-end (the import path):
// it proves a real AWX export imports, reconciles into a live Stratt control
// plane, and is LAUNCHED through the /api/v2 compat door as a governed Run
// (charter §5.6, ADR-0026). This is the in-process half — it needs a live
// Postgres but no cluster: Temporal and authz are faked at the seam, so it
// exercises import → reconcile → /api/v2 discover → launch → Run row without
// spawning an ansible pod. The ansible-pod execution half (the actuation that
// really runs a playbook on kind) runs under `task e2e:aap`.
//
// Flow: awxsim estate → Enumerate → Bundle → WriteBundle → ParseDir →
// desiredstate.Apply (Views) + UpsertWorkflow + DeclareCredentialRefAs →
// awxfacade.New over the same Store → GET /api/v2/job_templates/ (single-Step
// Workflows only) → POST .../launch/ → LaunchRun → one Temporal ExecuteWorkflow
// + one persisted Run bound to the imported View.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"

	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/core/internal/awxfacade"
	"github.com/dstout-devops/stratt/core/internal/desiredstate"
	"github.com/dstout-devops/stratt/core/internal/graph"
)

// e2eStore stands up a throwaway database (mirrors the compiler/triggers
// integration-test helper): CREATE DATABASE, migrate via graph.Connect, drop it
// WITH (FORCE) on cleanup. Skips (never fails) when no substrate is reachable.
func e2eStore(t *testing.T) *graph.Store {
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
	name := fmt.Sprintf("stratt_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close()
	})
	u, err := neturl.Parse(url)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	store, err := graph.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect+migrate test database: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

// fakeTemporal embeds client.Client (nil) and overrides ONLY ExecuteWorkflow to
// record the launch (LaunchRun discards the returned WorkflowRun, so nil is
// safe). Guarded by a mutex — the launch path is single-threaded here, but the
// interface allows concurrent callers.
type fakeTemporal struct {
	client.Client
	mu     sync.Mutex
	calls  int
	lastID string
}

func (f *fakeTemporal) ExecuteWorkflow(_ context.Context, opts client.StartWorkflowOptions, _ interface{}, _ ...interface{}) (client.WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastID = opts.ID
	return nil, nil
}

// allowAllAuthz is a permissive Authorizer — every Check passes, the backend is
// always healthy. It stands in for the OpenFGA seam so the e2e exercises the
// launch path without a policy fixture.
type allowAllAuthz struct{}

func (allowAllAuthz) Check(context.Context, string, string, string) (bool, error) { return true, nil }
func (allowAllAuthz) CheckHealth(context.Context) error                           { return nil }

var _ authz.Authorizer = allowAllAuthz{}

// TestMigrationE2E_ImportReconcileLaunch is the AAP-replacement import-path e2e.
func TestMigrationE2E_ImportReconcileLaunch(t *testing.T) {
	ctx := context.Background()

	// 1. Import: enumerate the canned AWX estate → emit → write → parse.
	snap := snapshotFromSim(t)
	emit, err := Bundle(snap, Options{})
	if err != nil {
		t.Fatalf("bundle: %v", err)
	}
	bundle := filepath.Join(t.TempDir(), "out")
	if err := WriteBundle(bundle, emit); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	decls, err := desiredstate.ParseDir(bundle, nil)
	if err != nil {
		t.Fatalf("parse imported bundle: %v", err)
	}

	// 2. Live control plane.
	store := e2eStore(t)

	// 3. Reconcile the imported estate into the graph.
	applied, err := desiredstate.Apply(ctx, store, desiredstate.Declarations{Views: decls.Views})
	if err != nil {
		t.Fatalf("apply views: %v", err)
	}
	for _, e := range applied.Entries {
		if e.Error != "" {
			t.Fatalf("apply view %s: %s", e.Name, e.Error)
		}
	}
	for _, wf := range decls.Workflows {
		if err := store.UpsertWorkflow(ctx, wf); err != nil {
			t.Fatalf("upsert workflow %s: %v", wf.Name, err)
		}
	}
	// The importer's declarations are Config-as-Code; declared_by is constrained
	// to {api, cac} at the DB layer, so the import path lands as cac (there is no
	// "awx-import" provenance value — adapting the test to the real constraint).
	for _, cr := range decls.CredentialRefs {
		if _, err := store.DeclareCredentialRefAs(ctx, cr, graph.DeclaredByCaC); err != nil {
			t.Fatalf("declare credential ref %s: %v", cr.Name, err)
		}
	}

	// 4-7. The /api/v2 façade over the same substrate; dev-principal auth.
	fake := &fakeTemporal{}
	h := awxfacade.New(awxfacade.Config{
		Store:              store,
		Temporal:           fake,
		Authz:              allowAllAuthz{},
		DevPrincipalHeader: true,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	do := func(method, path string, body []byte) *http.Response {
		t.Helper()
		var rdr *bytes.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req, err := http.NewRequest(method, srv.URL+path, rdr)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("X-Stratt-Principal", "e2e-tester")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	// 8. Discover: only single-Step Workflows surface as job_templates.
	resp := do("GET", "/api/v2/job_templates/", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list job_templates: status %d", resp.StatusCode)
	}
	var list struct {
		Count   int `json:"count"`
		Results []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode job_templates: %v", err)
	}
	resp.Body.Close()
	if list.Count != 2 || len(list.Results) != 2 {
		t.Fatalf("job_templates: count=%d results=%d, want 2 single-Step Workflows", list.Count, len(list.Results))
	}
	// The importer names Workflows "awx/<slug>" (namespaced); the two single-Step
	// job templates are Deploy Web + Gather Facts. The multi-Step workflow job
	// template prod-pipeline must NOT appear (it is a workflow_job_template).
	names := map[string]int64{}
	for _, r := range list.Results {
		names[r.Name] = r.ID
	}
	deployID, okDeploy := names["awx/deploy-web"]
	if _, okGather := names["awx/gather-facts"]; !okDeploy || !okGather {
		t.Fatalf("job_template names = %v, want awx/deploy-web + awx/gather-facts", names)
	}
	if _, present := names["awx/prod-pipeline"]; present {
		t.Fatal("multi-Step workflow_job_template awx/prod-pipeline must be filtered from job_templates")
	}

	// 9. Launch Deploy Web through the compat door.
	resp = do("POST", fmt.Sprintf("/api/v2/job_templates/%d/launch/", deployID), []byte(`{"extra_vars":{"greeting":"hello"}}`))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("launch: status %d (want 2xx)", resp.StatusCode)
	}
	var launched map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&launched); err != nil {
		t.Fatalf("decode launch response: %v", err)
	}
	resp.Body.Close()
	if launched["type"] != "job" {
		t.Fatalf("launch response shape: %+v", launched)
	}

	// 10. Exactly one Temporal workflow started.
	fake.mu.Lock()
	calls, lastID := fake.calls, fake.lastID
	fake.mu.Unlock()
	if calls != 1 {
		t.Fatalf("ExecuteWorkflow calls = %d, want 1", calls)
	}

	// 11. A Run row now exists, bound to the imported View.
	runs, err := store.ListRuns(ctx, 0)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want exactly 1 launched Run", len(runs))
	}
	run := runs[0]
	if !strings.HasPrefix(run.ViewRef, "view://") {
		t.Fatalf("Run must bind the imported View, got ViewRef=%q", run.ViewRef)
	}
	if lastID != "run-"+run.ID {
		t.Fatalf("started workflow id %q must be run-%s", lastID, run.ID)
	}

	// 12. Both imported CredentialRefs are declared under the import (cac) path.
	crefs, err := store.ListCredentialRefsDeclaredBy(ctx, graph.DeclaredByCaC)
	if err != nil {
		t.Fatalf("list credential refs: %v", err)
	}
	if len(crefs) != 2 {
		t.Fatalf("credential refs declared by awx-import = %d, want 2", len(crefs))
	}
}
