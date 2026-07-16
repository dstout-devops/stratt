package cellrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestFanoutNoRecursion proves an AUTHENTICATED peer fan-out call is served
// local-only (no recursion into peers).
func TestFanoutNoRecursion(t *testing.T) {
	secret := []byte("fleet-secret")
	peerCalled := false
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerCalled = true
		_, _ = io.WriteString(w, "[]")
	}))
	defer peer.Close()
	rt := Middleware(localInner(runsBody([2]string{"r1", "2026-01-01T00:00:00Z"}), "[]"),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local", Secret: secret})
	req := httptest.NewRequest(http.MethodGet, "/runs", nil)
	req.Header.Set(fanoutHeader, "1")
	req.Header.Set(authHeader, signCellAuth(secret, http.MethodGet, "/runs", "", time.Now().Unix()))
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
	if rec.Code != http.StatusPartialContent || rec.Header().Get(hdrSkewed) != "eu" {
		t.Fatalf("a merge failure must be 206 + named skewed peer, got %d skewed=%q", rec.Code, rec.Header().Get(hdrSkewed))
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

// TestFanoutAuthValid proves an authenticated fan-out call (valid HMAC in
// window) is served local-only, not re-federated (ADR-0044 slice 4).
func TestFanoutAuthValid(t *testing.T) {
	secret := []byte("fleet-secret")
	rt := Middleware(localInner(runsBody([2]string{"r1", "2026-01-01T00:00:00Z"}), "[]"),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: "http://unused"}}}, CellID: "local", Secret: secret})
	req := httptest.NewRequest(http.MethodGet, "/runs", nil)
	req.Header.Set(fanoutHeader, "1")
	req.Header.Set(authHeader, signCellAuth(secret, http.MethodGet, "/runs", "", time.Now().Unix()))
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get(hdrQueried) != "" {
		t.Fatalf("authenticated fan-out must be served local-only, got %d queried=%q", rec.Code, rec.Header().Get(hdrQueried))
	}
}

// TestFanoutAuthRejected proves a fanout header WITHOUT a valid signature is a
// 401 — a spoof or a misconfigured peer, never honored (§1.8 loud-fail).
func TestFanoutAuthRejected(t *testing.T) {
	secret := []byte("fleet-secret")
	rt := Middleware(localInner("[]", "[]"),
		Deps{Store: fakeCells{nil}, CellID: "local", Secret: secret})
	// Missing signature.
	req := httptest.NewRequest(http.MethodGet, "/runs", nil)
	req.Header.Set(fanoutHeader, "1")
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned fanout must be 401, got %d", rec.Code)
	}
	// Tampered signature (wrong secret).
	req2 := httptest.NewRequest(http.MethodGet, "/runs", nil)
	req2.Header.Set(fanoutHeader, "1")
	req2.Header.Set(authHeader, signCellAuth([]byte("wrong"), http.MethodGet, "/runs", "", time.Now().Unix()))
	rec2 := httptest.NewRecorder()
	rt.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("bad-secret fanout must be 401, got %d", rec2.Code)
	}
	// Expired signature (outside the window).
	req3 := httptest.NewRequest(http.MethodGet, "/runs", nil)
	req3.Header.Set(fanoutHeader, "1")
	req3.Header.Set(authHeader, signCellAuth(secret, http.MethodGet, "/runs", "", time.Now().Add(-time.Hour).Unix()))
	rec3 := httptest.NewRecorder()
	rt.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("expired fanout must be 401, got %d", rec3.Code)
	}
}

// TestFanoutSignedOutbound proves the router signs its outbound peer calls so a
// peer can authenticate them.
func TestFanoutSignedOutbound(t *testing.T) {
	secret := []byte("fleet-secret")
	var gotAuth string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get(authHeader)
		_, _ = io.WriteString(w, "[]")
	}))
	defer peer.Close()
	rt := Middleware(localInner("[]", "[]"),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local", Secret: secret})
	rt.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/runs", nil))
	if gotAuth == "" || !verifyCellAuth(secret, http.MethodGet, "/runs", "", gotAuth, defaultReplayWindow) {
		t.Fatalf("peer must receive a valid fan-out signature, got %q", gotAuth)
	}
}

// skewPeer serves /cellinfo (issuer/audience) and /runs (with a registry-version
// header) — for exercising the §1.6/§1.5 skew gates.
func skewPeer(issuer, audience, regVersion string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/cellinfo":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"issuer":"`+issuer+`","audience":"`+audience+`"}`)
		case "/api/v1/runs":
			w.Header().Set(hdrRegistryVersion, regVersion)
			_, _ = io.WriteString(w, runsBody([2]string{"peer1", "2026-06-01T00:00:00Z"}))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// TestSkewGateIssuer proves a peer advertising a DIFFERENT OIDC issuer is named
// skewed + 206, its data never merged and its token never forwarded (§1.6).
func TestSkewGateIssuer(t *testing.T) {
	peer := skewPeer("other-issuer", "aud", "v1")
	defer peer.Close()
	rt := Middleware(localInner(runsBody([2]string{"local1", "2026-01-01T00:00:00Z"}), "[]"),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local",
			Issuer: "local-issuer", Audience: "aud", RegistryVersion: "v1"})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs", nil))
	if rec.Code != http.StatusPartialContent || rec.Header().Get(hdrSkewed) != "eu" {
		t.Fatalf("issuer-mismatched peer must be named skewed + 206, got %d skewed=%q", rec.Code, rec.Header().Get(hdrSkewed))
	}
	var runs []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &runs)
	if len(runs) != 1 || runs[0]["id"] != "local1" {
		t.Fatalf("skewed peer's data must NOT be merged; got %s", rec.Body.String())
	}
}

// TestSkewGateRegistry proves a peer with a matching issuer but a DIVERGENT
// Contract registry version is named skewed + 206 (§1.5).
func TestSkewGateRegistry(t *testing.T) {
	peer := skewPeer("shared-issuer", "aud", "v2") // registry v2 ≠ local v1
	defer peer.Close()
	rt := Middleware(localInner(runsBody([2]string{"local1", "2026-01-01T00:00:00Z"}), "[]"),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local",
			Issuer: "shared-issuer", Audience: "aud", RegistryVersion: "v1"})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs", nil))
	if rec.Code != http.StatusPartialContent || rec.Header().Get(hdrSkewed) != "eu" {
		t.Fatalf("registry-mismatched peer must be named skewed + 206, got %d skewed=%q", rec.Code, rec.Header().Get(hdrSkewed))
	}
}

// TestSkewGateMatch proves a peer with matching issuer + registry federates
// cleanly (200, no skew).
func TestSkewGateMatch(t *testing.T) {
	peer := skewPeer("shared-issuer", "aud", "v1")
	defer peer.Close()
	rt := Middleware(localInner(runsBody([2]string{"local1", "2026-01-01T00:00:00Z"}), "[]"),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local",
			Issuer: "shared-issuer", Audience: "aud", RegistryVersion: "v1"})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs", nil))
	if rec.Code != http.StatusOK || rec.Header().Get(hdrSkewed) != "" {
		t.Fatalf("matching peer must federate cleanly (200), got %d skewed=%q", rec.Code, rec.Header().Get(hdrSkewed))
	}
	var runs []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &runs)
	if len(runs) != 2 {
		t.Fatalf("matching peer's data must merge (2 runs), got %s", rec.Body.String())
	}
}

// TestFanoutIgnoredWithoutSecret proves a single-Cell daemon (no secret) strips
// an inbound fanout header rather than honoring it (no spoof-to-local-only).
func TestFanoutIgnoredWithoutSecret(t *testing.T) {
	rt := Middleware(localInner(runsBody([2]string{"r1", "2026-01-01T00:00:00Z"}), "[]"),
		Deps{Store: fakeCells{nil}, CellID: "local"}) // no Secret, no peers
	req := httptest.NewRequest(http.MethodGet, "/runs", nil)
	req.Header.Set(fanoutHeader, "1")
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("no-secret single-cell must serve normally (fanout ignored), got %d", rec.Code)
	}
}

// pathPeer serves a fixed path→body map (with /api/v1 prefix).
func pathPeer(paths map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := paths[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func pathInner(paths map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := paths[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
}

// TestFederatedAudit proves /audit merges across Cells on `at` DESC (per-Cell
// seq is not comparable) with cell on every element (ADR-0044 slice 4).
func TestFederatedAudit(t *testing.T) {
	local := `[{"seq":2,"at":"2026-01-02T00:00:00Z","action":"a","cell":"local"},{"seq":1,"at":"2026-01-01T00:00:00Z","action":"a","cell":"local"}]`
	peer := pathPeer(map[string]string{"/api/v1/audit": `[{"seq":9,"at":"2026-06-01T00:00:00Z","action":"a","cell":"eu"}]`})
	defer peer.Close()
	rt := Middleware(pathInner(map[string]string{"/audit": local}),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local"}) // no issuer/registry gates
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/audit", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("all Cells answered → 200, got %d", rec.Code)
	}
	var evs []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &evs); err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || evs[0]["cell"] != "eu" || evs[1]["cell"] != "local" || evs[2]["cell"] != "local" {
		t.Fatalf("audit must merge at-DESC with cell on the wire, got %v", evs)
	}
}

// TestFederatedUsage proves /usage SUMs (principal,tool) across Cells and MAXes
// lastCall — a scatter-gather-SUM, never a duplicate row (ADR-0044 slice 4).
func TestFederatedUsage(t *testing.T) {
	local := `[{"principal":"alice","tool":"x","calls":2,"errors":0,"lastCall":"2026-01-01T00:00:00Z"},{"principal":"bob","tool":"y","calls":1,"errors":0,"lastCall":"2026-01-01T00:00:00Z"}]`
	peer := pathPeer(map[string]string{"/api/v1/usage": `[{"principal":"alice","tool":"x","calls":3,"errors":1,"lastCall":"2026-05-01T00:00:00Z"}]`})
	defer peer.Close()
	rt := Middleware(pathInner(map[string]string{"/usage": local}),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local"})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/usage", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("all Cells answered → 200, got %d", rec.Code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	// sorted by key: alice|x then bob|y.
	if len(rows) != 2 {
		t.Fatalf("usage must group (no duplicate (principal,tool) rows), got %v", rows)
	}
	if rows[0]["principal"] != "alice" || rows[0]["calls"].(float64) != 5 || rows[0]["errors"].(float64) != 1 || rows[0]["lastCall"] != "2026-05-01T00:00:00Z" {
		t.Fatalf("alice|x must SUM calls=5 errors=1 MAX lastCall, got %v", rows[0])
	}
	if rows[1]["principal"] != "bob" || rows[1]["calls"].(float64) != 1 {
		t.Fatalf("bob|y must pass through, got %v", rows[1])
	}
}

// TestFederatedAuditRejectsSinceCursor proves the seq-based /audit cursor is
// rejected loudly on a multi-Cell estate (it's incoherent cross-Cell) rather
// than serving a silently-broken page (§1.8).
func TestFederatedAuditRejectsSinceCursor(t *testing.T) {
	peer := pathPeer(map[string]string{"/api/v1/audit": "[]"})
	defer peer.Close()
	rt := Middleware(pathInner(map[string]string{"/audit": "[]"}),
		Deps{Store: fakeCells{[]types.Cell{{Name: "eu", Endpoint: peer.URL}}}, CellID: "local"})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/audit?since=5", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a cross-Cell /audit with 'since' must 400, got %d", rec.Code)
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
