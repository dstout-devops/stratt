package controller

// The poll-cost budget (ADR-0131), asserted by COUNTING REQUESTS. Three ADRs each added a
// read to this sync and none owned the total; these tests are what stops a fourth from
// doing the same quietly.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingAWX serves the whole surface and records every path it was asked for, so a test
// can assert the SHAPE of a poll rather than trust a comment about it. failDetail makes
// every per-object sub-read fail while the collections keep working.
type countingAWX struct {
	mu         sync.Mutex
	paths      []string
	failDetail bool
}

func (a *countingAWX) hits(substr string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	var n int
	for _, p := range a.paths {
		if strings.Contains(p, substr) {
			n++
		}
	}
	return n
}

func (a *countingAWX) total() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.paths)
}

func (a *countingAWX) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.paths = nil
}

func (a *countingAWX) server(t *testing.T) *httptest.Server {
	t.Helper()
	page := func(results any) []byte {
		b, _ := json.Marshal(map[string]any{"next": "", "results": results})
		return b
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.paths = append(a.paths, r.URL.Path)
		a.mu.Unlock()

		isDetail := strings.Contains(r.URL.Path, "workflow_nodes") || strings.HasSuffix(r.URL.Path, "/users/") && strings.Contains(r.URL.Path, "/teams/")
		if isDetail && a.failDetail {
			http.Error(w, `{"detail":"boom"}`, http.StatusInternalServerError)
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "workflow_nodes"):
			w.Write(page([]map[string]any{{
				"id": 100, "unified_job_template": 10,
				"summary_fields": map[string]any{"unified_job_template": map[string]any{"id": 10, "unified_job_type": "job"}},
			}}))
		case strings.HasPrefix(r.URL.Path, "/api/v2/teams/") && strings.HasSuffix(r.URL.Path, "/users/"):
			w.Write(page([]map[string]any{{"id": 60, "username": "admin"}}))
		case r.URL.Path == "/api/v2/workflow_job_templates/":
			w.Write(page([]map[string]any{{"id": 20, "name": "pipeline"}}))
		case r.URL.Path == "/api/v2/teams/":
			w.Write(page([]map[string]any{{"id": 1, "name": "web-ops"}}))
		case r.URL.Path == "/api/v2/job_templates/":
			w.Write(page([]map[string]any{{"id": 10, "name": "Deploy"}}))
		default:
			w.Write(page([]map[string]any{}))
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// clockClient builds a client whose clock the test drives.
func clockClient(t *testing.T, url string, detail time.Duration, now *time.Time) *Client {
	t.Helper()
	c := New(Config{Endpoint: url, Token: "t", ControllerID: "ctrl-a", DetailInterval: detail})
	c.now = func() time.Time { return *now }
	return c
}

// D1: the COLLECTIONS run every poll; the expensive per-object tier does not. The second
// poll inside the detail interval must cost exactly the collection count and no more.
//
// collectionReads is asserted as a literal on purpose. Four ADRs have now added reads to
// this sync, and this number moving is the signal that a fifth did — ADR-0131's whole
// point is that the total has an owner. Bump it deliberately, never to make a test pass.
const collectionReads = 9 // job_templates, workflow_jts, schedules, orgs, teams, credentials, users, labels, execution_environments

func TestDetailTierIsNotReadEveryPoll(t *testing.T) {
	awx := &countingAWX{}
	srv := awx.server(t)
	clock := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	c := clockClient(t, srv.URL, 5*time.Minute, &clock)

	if _, err := c.Enumerate(context.Background()); err != nil {
		t.Fatalf("first enumerate: %v", err)
	}
	// First sync always reads detail — an empty cache is a miss, not a reason to project
	// a workflow with no edges. 7 collections + 1 workflow + 1 team.
	if got, want := awx.total(), collectionReads+2; got != want {
		t.Fatalf("first poll issued %d requests, want %d (%d collections + 1 workflow + 1 team of detail)", got, want, collectionReads)
	}

	awx.reset()
	clock = clock.Add(time.Minute) // inside the detail interval
	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("second enumerate: %v", err)
	}
	if got := awx.total(); got != collectionReads {
		t.Errorf("second poll issued %d requests, want %d — the detail tier must be served from cache (ADR-0131 D1)", got, collectionReads)
	}
	if awx.hits("workflow_nodes") != 0 {
		t.Error("workflow nodes were re-read inside the detail interval")
	}
	// Served from cache means the EDGES ARE STILL THERE — the point of caching rather
	// than omitting: omitting would retract and re-create real edges every cycle.
	if len(snap.WorkflowNodes[20]) != 1 {
		t.Errorf("cached workflow nodes lost: %+v", snap.WorkflowNodes)
	}
	if len(snap.TeamMembers[1]) != 1 {
		t.Errorf("cached team members lost: %+v", snap.TeamMembers)
	}
	if snap.Partial {
		t.Error("a cache-served cycle must not be Partial — nothing failed")
	}

	// Past the interval, detail refreshes again.
	awx.reset()
	clock = clock.Add(10 * time.Minute)
	if _, err := c.Enumerate(context.Background()); err != nil {
		t.Fatalf("third enumerate: %v", err)
	}
	if awx.hits("workflow_nodes") != 1 {
		t.Errorf("detail did not refresh after the interval elapsed (workflow_nodes hits = %d)", awx.hits("workflow_nodes"))
	}
}

// D2: a failed DETAIL read degrades the cycle — it must not lose the collections that
// succeeded. This is the case that used to throw away six good reads because of one bad
// seventy-first.
func TestFailedDetailReadDegradesRatherThanFails(t *testing.T) {
	awx := &countingAWX{failDetail: true}
	srv := awx.server(t)
	clock := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	c := clockClient(t, srv.URL, 5*time.Minute, &clock)

	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("a failing DETAIL read must not fail the Observe (ADR-0131 D2): %v", err)
	}
	if !snap.Partial {
		t.Fatal("snapshot is not marked Partial — the server would assert a full-sync boundary and the host would sweep on the strength of an unfinished read")
	}
	// The collections that DID succeed are all present.
	if len(snap.JobTemplates) != 1 || len(snap.Workflows) != 1 || len(snap.Teams) != 1 {
		t.Errorf("collections lost to a detail failure: %d templates, %d workflows, %d teams", len(snap.JobTemplates), len(snap.Workflows), len(snap.Teams))
	}
}

// A degraded pass must NOT reset the detail clock, or a single flaky cycle would sit on
// partial data for a whole interval instead of retrying on the next poll.
func TestDegradedDetailPassRetriesNextPoll(t *testing.T) {
	awx := &countingAWX{failDetail: true}
	srv := awx.server(t)
	clock := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	c := clockClient(t, srv.URL, 5*time.Minute, &clock)

	if _, err := c.Enumerate(context.Background()); err != nil {
		t.Fatalf("first enumerate: %v", err)
	}
	awx.reset()
	awx.failDetail = false
	clock = clock.Add(time.Minute) // well inside the interval

	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("second enumerate: %v", err)
	}
	if awx.hits("workflow_nodes") != 1 {
		t.Fatalf("detail was not retried on the next poll after a degraded pass (hits = %d) — a flaky cycle must not cost a whole interval", awx.hits("workflow_nodes"))
	}
	if snap.Partial {
		t.Error("the retry succeeded, so the cycle must no longer be Partial")
	}
	if got := c.DetailAge(); got != 0 {
		t.Errorf("DetailAge = %v after a clean refresh, want 0", got)
	}
}

// The cache is pruned to what this cycle enumerated, so a deleted workflow's nodes do not
// linger and get re-asserted forever.
func TestDetailCachePrunesDeletedObjects(t *testing.T) {
	var workflows []map[string]any = []map[string]any{{"id": 20, "name": "pipeline"}}
	var mu sync.Mutex
	page := func(results any) []byte {
		b, _ := json.Marshal(map[string]any{"next": "", "results": results})
		return b
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/api/v2/workflow_job_templates/":
			w.Write(page(workflows))
		case strings.Contains(r.URL.Path, "workflow_nodes"):
			w.Write(page([]map[string]any{{
				"id": 100, "unified_job_template": 10,
				"summary_fields": map[string]any{"unified_job_template": map[string]any{"id": 10, "unified_job_type": "job"}},
			}}))
		default:
			w.Write(page([]map[string]any{}))
		}
	}))
	t.Cleanup(srv.Close)

	clock := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	c := clockClient(t, srv.URL, time.Minute, &clock)
	if _, err := c.Enumerate(context.Background()); err != nil {
		t.Fatalf("first enumerate: %v", err)
	}

	mu.Lock()
	workflows = nil // the workflow is deleted at AWX
	mu.Unlock()
	clock = clock.Add(2 * time.Minute)

	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("second enumerate: %v", err)
	}
	if len(snap.WorkflowNodes) != 0 {
		t.Errorf("deleted workflow's nodes survived in the cache: %+v", snap.WorkflowNodes)
	}
}
