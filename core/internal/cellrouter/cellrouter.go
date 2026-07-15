// Package cellrouter is the read-federation tier of the control-plane Cells
// architecture (ADR-0044 slice 3). It is a stateless middleware compiled into
// every strattd that scatter-gathers list/point reads across Cell peers and
// merges them into one logical estate, with partial-result honesty: an
// unreachable Cell is NAMED in a response header and the status becomes 206 —
// never silently dropped (§1.8). A write is never forwarded here (§2.4: writes
// go to a datum's home Cell; that is slice 5). A single-Cell "local" deployment
// (no graph.cell peers) is a byte-identical pass-through.
package cellrouter

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// fanoutHeader marks a request that is itself a cross-Cell fan-out call: the
// receiving Cell serves it LOCAL-ONLY, so peers never recurse into each other.
const fanoutHeader = "X-Stratt-Cell-Fanout"

const (
	hdrQueried     = "X-Stratt-Cells-Queried"
	hdrUnreachable = "X-Stratt-Cells-Unreachable"
)

// CellLister is the peer-registry read the router needs (satisfied by
// *graph.Store) — kept as an interface so cellrouter imports neither graph nor
// api (api imports cellrouter; this avoids the cycle).
type CellLister interface {
	ListCells(ctx context.Context) ([]types.Cell, error)
}

// Deps are the router's dependencies.
type Deps struct {
	Store  CellLister
	CellID string // this daemon's own Cell (STRATT_CELL_ID); excluded from the peer set.
	Log    *slog.Logger
}

type router struct {
	inner http.Handler
	deps  Deps
	http  *http.Client

	mu      sync.Mutex
	peers   []types.Cell
	peersAt time.Time
}

// Middleware wraps the generated API router with read federation. inner is the
// generated router (served local-only for a fan-out or a single-Cell estate).
func Middleware(inner http.Handler, deps Deps) http.Handler {
	if deps.CellID == "" {
		deps.CellID = types.LocalCell
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	return &router{inner: inner, deps: deps, http: &http.Client{Timeout: 5 * time.Second}}
}

func (rt *router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A peer's fan-out call: serve local-only, never re-federate (no recursion).
	if r.Header.Get(fanoutHeader) != "" {
		rt.inner.ServeHTTP(w, r)
		return
	}
	ad, kind := classify(r)
	if kind == kindNone {
		rt.inner.ServeHTTP(w, r)
		return
	}
	peers := rt.peerSet(r.Context())
	if len(peers) == 0 {
		// Single-Cell estate: byte-identical pass-through (the no-op tripwire).
		rt.inner.ServeHTTP(w, r)
		return
	}
	switch kind {
	case kindList:
		rt.federateList(w, r, ad, peers)
	case kindPoint:
		rt.federatePoint(w, r, peers)
	}
}

// peerSet returns the declared Cells other than self, cached briefly so a
// single-Cell estate pays at most one ListCells query per TTL (not per request).
func (rt *router) peerSet(ctx context.Context) []types.Cell {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !rt.peersAt.IsZero() && time.Since(rt.peersAt) < 5*time.Second {
		return rt.peers
	}
	cells, err := rt.deps.Store.ListCells(ctx)
	if err != nil {
		rt.deps.Log.Warn("cellrouter: peer set unavailable; keeping cached", "error", err)
		return rt.peers
	}
	peers := make([]types.Cell, 0, len(cells))
	for _, c := range cells {
		if c.Name != rt.deps.CellID {
			peers = append(peers, c)
		}
	}
	rt.peers, rt.peersAt = peers, time.Now()
	return peers
}

// federateList scatter-gathers a stable list read: serve local, fan out to
// peers concurrently (forwarding the caller's auth), merge in total order,
// truncate to limit, and write 200 (all answered) or 206 (a Cell unreachable).
func (rt *router) federateList(w http.ResponseWriter, r *http.Request, ad adapter, peers []types.Cell) {
	rec := httptest.NewRecorder()
	rt.inner.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		copyResp(w, rec) // a local error is returned as-is, not federated
		return
	}
	bodies := [][]byte{rec.Body.Bytes()}
	queried := []string{rt.deps.CellID}
	var unreachable []string
	fwd := forwardHeaders(r)

	type res struct {
		name string
		body []byte
		ok   bool
	}
	ch := make(chan res, len(peers))
	for _, c := range peers {
		go func(c types.Cell) {
			status, body, err := rt.peerGet(r.Context(), c.Endpoint, r.URL.Path, r.URL.RawQuery, fwd)
			ch <- res{c.Name, body, err == nil && status == http.StatusOK}
		}(c)
	}
	for range peers {
		x := <-ch
		queried = append(queried, x.name)
		if x.ok {
			bodies = append(bodies, x.body)
		} else {
			unreachable = append(unreachable, x.name)
		}
	}

	merged, err := mergeList(bodies, ad, limitOf(r))
	if err != nil {
		// A peer body was unparseable (e.g. registry/schema skew). Return the
		// LOCAL data but NEVER a clean 200 (§1.8): name every peer as
		// unreachable and 206 — a merge that couldn't incorporate a peer is a
		// partial result, not a complete one.
		rt.deps.Log.Error("cellrouter: merge failed; returning local subset as partial", "error", err)
		all := make([]string, 0, len(peers))
		for _, c := range peers {
			all = append(all, c.Name)
		}
		writePartial(w, queried, all, rec.Body.Bytes())
		return
	}
	writePartial(w, queried, unreachable, merged)
}

// federatePoint routes a single-Entity read: local is authoritative if present
// (single-writer residency), else ask peers for the one Cell that homes it.
func (rt *router) federatePoint(w http.ResponseWriter, r *http.Request, peers []types.Cell) {
	rec := httptest.NewRecorder()
	rt.inner.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		copyResp(w, rec) // local hit (200) is authoritative; other errors pass through
		return
	}
	fwd := forwardHeaders(r)
	var unreachable []string
	for _, c := range peers {
		status, body, err := rt.peerGet(r.Context(), c.Endpoint, r.URL.Path, r.URL.RawQuery, fwd)
		if err != nil {
			unreachable = append(unreachable, c.Name)
			continue
		}
		if status == http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
	}
	// If a Cell was unreachable, a 404 would be a LIE (the Entity may home there),
	// so return 503 — "could not determine existence, a Cell is down" — never a
	// definitive not-found (§1.8). A header-ignoring REST client sees a non-2xx;
	// the MCP surface renders the message as a tool error, not "does not exist".
	if len(unreachable) > 0 {
		sort.Strings(unreachable)
		w.Header().Set(hdrUnreachable, strings.Join(unreachable, ","))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"entity not found in reachable Cells; unreachable: ` + strings.Join(unreachable, ",") + ` — it may reside there"}`))
		return
	}
	// Genuinely absent from every reachable Cell, none unreachable → honest 404.
	copyResp(w, rec)
}

func writePartial(w http.ResponseWriter, queried, unreachable []string, body []byte) {
	sort.Strings(queried)
	w.Header().Set(hdrQueried, strings.Join(queried, ","))
	code := http.StatusOK
	if len(unreachable) > 0 {
		sort.Strings(unreachable)
		w.Header().Set(hdrUnreachable, strings.Join(unreachable, ","))
		code = http.StatusPartialContent
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

func copyResp(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	for k, vs := range rec.Header() {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}

func limitOf(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return n
}
