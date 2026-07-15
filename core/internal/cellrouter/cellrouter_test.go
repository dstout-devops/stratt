package cellrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

type fakeCells struct{ cells []types.Cell }

func (f fakeCells) ListCells(context.Context) ([]types.Cell, error) { return f.cells, nil }

// runsBody builds a /runs JSON array from (id, rfc3339-startedAt) pairs.
func runsBody(pairs ...[2]string) string {
	var out []map[string]any
	for _, p := range pairs {
		out = append(out, map[string]any{"id": p[0], "startedAt": p[1], "status": "succeeded", "workflowId": "w"})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// peerServer stands up a stub peer strattd API; it records the last forwarded
// auth header and serves the given /runs and /findings bodies.
func peerServer(runs, findings string) (*httptest.Server, *string) {
	var lastAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/runs":
			_, _ = io.WriteString(w, runs)
		case "/api/v1/findings":
			_, _ = io.WriteString(w, findings)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, &lastAuth
}

// localInner is the stub "generated router" for the local Cell.
func localInner(runs, findings string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/runs":
			_, _ = io.WriteString(w, runs)
		case r.Method == http.MethodGet && r.URL.Path == "/findings":
			_, _ = io.WriteString(w, findings)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// TestFederatedMergeAndLimit proves the k-way merge is total-ordered
// (started_at DESC, id ASC) across Cells and respects limit; status 200.
func TestFederatedMergeAndLimit(t *testing.T) {
	peer, _ := peerServer(runsBody([2]string{"r2", "2026-01-02T00:00:00Z"}, [2]string{"r0", "2026-01-00T00:00:00Z"}), "[]")
	defer peer.Close()
	rt := Middleware(
		localInner(runsBody([2]string{"r3", "2026-01-03T00:00:00Z"}, [2]string{"r1", "2026-01-01T00:00:00Z"}), "[]"),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local"},
	)

	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs?limit=3", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("all Cells answered → 200, got %d", rec.Code)
	}
	if got := rec.Header().Get(hdrQueried); got != "eu,local" {
		t.Fatalf("queried header must name both Cells sorted, got %q", got)
	}
	var runs []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, r := range runs {
		ids = append(ids, r["id"].(string))
	}
	if strings.Join(ids, ",") != "r3,r2,r1" {
		t.Fatalf("merged order (started_at DESC) truncated to limit=3 must be r3,r2,r1, got %v", ids)
	}
}

// TestPartialResult206 proves an unreachable Cell yields 206 + a named header +
// the valid local subset (§1.8 — named, never dropped).
func TestPartialResult206(t *testing.T) {
	peer, _ := peerServer("[]", "[]")
	peer.Close() // unreachable
	rt := Middleware(
		localInner("[]", runsBody([2]string{"f1", "2026-01-01T00:00:00Z"})),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local"},
	)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/findings", nil))
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("an unreachable Cell must yield 206, got %d", rec.Code)
	}
	if got := rec.Header().Get(hdrUnreachable); got != "eu" {
		t.Fatalf("unreachable Cell must be NAMED, got %q", got)
	}
	var fs []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &fs); err != nil || len(fs) != 1 {
		t.Fatalf("partial body must still carry the local subset, got %s err=%v", rec.Body.String(), err)
	}
}

// TestAuthForward proves the caller's token is replayed to the peer (§1.6).
func TestAuthForward(t *testing.T) {
	peer, lastAuth := peerServer("[]", "[]")
	defer peer.Close()
	rt := Middleware(localInner("[]", "[]"),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local"})
	req := httptest.NewRequest(http.MethodGet, "/runs", nil)
	req.Header.Set("Authorization", "Bearer alice-token")
	rt.ServeHTTP(httptest.NewRecorder(), req)
	if *lastAuth != "Bearer alice-token" {
		t.Fatalf("peer must receive the forwarded caller token, got %q", *lastAuth)
	}
}

// TestFanoutNoRecursion proves a peer's fan-out call is served local-only.
func TestFanoutNoRecursion(t *testing.T) {
	peerCalled := false
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerCalled = true
		_, _ = io.WriteString(w, "[]")
	}))
	defer peer.Close()
	rt := Middleware(localInner(runsBody([2]string{"r1", "2026-01-01T00:00:00Z"}), "[]"),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local"})
	req := httptest.NewRequest(http.MethodGet, "/runs", nil)
	req.Header.Set(fanoutHeader, "1")
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if peerCalled {
		t.Fatal("a fan-out request must be served local-only (no recursion into peers)")
	}
	if rec.Header().Get(hdrQueried) != "" {
		t.Fatal("a fan-out request must not emit federation headers")
	}
}

// TestSingleCellNoop proves an estate with no peers is a byte-identical
// pass-through — the tripwire guarding the no-op invariant.
func TestSingleCellNoop(t *testing.T) {
	inner := localInner(runsBody([2]string{"r1", "2026-01-01T00:00:00Z"}), "[]")
	rt := Middleware(inner, Deps{Store: fakeCells{nil}, CellID: "local"})

	fedRec := httptest.NewRecorder()
	rt.ServeHTTP(fedRec, httptest.NewRequest(http.MethodGet, "/runs", nil))
	rawRec := httptest.NewRecorder()
	inner.ServeHTTP(rawRec, httptest.NewRequest(http.MethodGet, "/runs", nil))

	if fedRec.Code != rawRec.Code || fedRec.Body.String() != rawRec.Body.String() {
		t.Fatalf("single-Cell must be byte-identical to inner: fed=%d/%q raw=%d/%q",
			fedRec.Code, fedRec.Body.String(), rawRec.Code, rawRec.Body.String())
	}
	if fedRec.Header().Get(hdrQueried) != "" {
		t.Fatal("single-Cell must emit no federation headers")
	}
}

// TestMergeErrorIsPartial proves an unparseable peer body (schema/version skew)
// yields 206 + named peers, never a clean 200 that hides the drop (§1.8).
func TestMergeErrorIsPartial(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"not":"an array"}`) // unmergeable
	}))
	defer peer.Close()
	rt := Middleware(localInner(runsBody([2]string{"r1", "2026-01-01T00:00:00Z"}), "[]"),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local"})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs", nil))
	if rec.Code != http.StatusPartialContent || rec.Header().Get(hdrUnreachable) != "eu" {
		t.Fatalf("a merge failure must be 206 + named peer, got %d unreachable=%q", rec.Code, rec.Header().Get(hdrUnreachable))
	}
	var runs []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil || len(runs) != 1 {
		t.Fatalf("partial body must be the local subset, got %s", rec.Body.String())
	}
}

// TestPointReadUnreachable503 proves a point-read miss with an unreachable Cell
// returns 503 (not a lying 404), naming the unreachable Cell (§1.8).
func TestPointReadUnreachable503(t *testing.T) {
	peer, _ := peerServer("[]", "[]")
	peer.Close()
	rt := Middleware(localInner("[]", "[]"),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local"})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/entities/abc", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get(hdrUnreachable) != "eu" {
		t.Fatalf("point-read with an unreachable home Cell must be 503 + named, got %d unreachable=%q", rec.Code, rec.Header().Get(hdrUnreachable))
	}
}

// TestPointReadGenuine404 proves a miss with all Cells reachable is an honest 404.
func TestPointReadGenuine404(t *testing.T) {
	peer, _ := peerServer("[]", "[]") // reachable; returns 404 for the entity
	defer peer.Close()
	rt := Middleware(localInner("[]", "[]"),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local"})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/entities/abc", nil))
	if rec.Code != http.StatusNotFound || rec.Header().Get(hdrUnreachable) != "" {
		t.Fatalf("genuine miss (all reachable) must be a plain 404, got %d unreachable=%q", rec.Code, rec.Header().Get(hdrUnreachable))
	}
}

// TestNonFederatedPassthrough proves writes/single-reads are never federated.
func TestNonFederatedPassthrough(t *testing.T) {
	peerCalled := false
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { peerCalled = true }))
	defer peer.Close()
	rt := Middleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusCreated) }),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local"})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/runs", nil)) // a write
	if peerCalled || rec.Code != http.StatusCreated {
		t.Fatalf("a write must pass straight to inner, never federate (peerCalled=%v code=%d)", peerCalled, rec.Code)
	}
}
