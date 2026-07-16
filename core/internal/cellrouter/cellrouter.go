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
	"encoding/json"
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

// authHeader carries the HMAC signature proving a fan-out call came from a peer
// Cell holding the shared STRATT_CELL_SECRET (ADR-0044 slice 4). Format:
// "<unix-ts>:<hex-hmac-sha256(method\npath\nrawQuery\nts)>".
const authHeader = "X-Stratt-Cell-Auth"

const defaultReplayWindow = 30 * time.Second

const (
	hdrQueried         = "X-Stratt-Cells-Queried"
	hdrUnreachable     = "X-Stratt-Cells-Unreachable"
	hdrSkewed          = "X-Stratt-Cells-Skewed"
	hdrRegistryVersion = "X-Stratt-Registry-Version"
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
	// Secret is the fleet-wide STRATT_CELL_SECRET used to HMAC-authenticate
	// cross-Cell fan-out calls (ADR-0044 slice 4). Empty ⇒ single-Cell: no
	// signing, and an inbound fanout header is ignored (never honored).
	Secret []byte
	// ReplayWindow bounds the age of a fan-out signature (default 30s).
	ReplayWindow time.Duration
	// Issuer/Audience/RegistryVersion are this Cell's identity + registry
	// coordinates, compared against each peer's /cellinfo + response header to
	// BLOCK federating a peer on a divergent OIDC issuer or Contract registry
	// (§1.5/§1.6, ADR-0044 slice 4) — a skewed peer is NAMED, never a silent
	// union, and its bearer is never forwarded.
	Issuer          string
	Audience        string
	RegistryVersion string
}

// peer is a discovered peer Cell plus the result of the discovery-time issuer
// probe (§1.6 shared-issuer gate, ADR-0044 slice 4).
type peer struct {
	name     string
	endpoint string
	issuerOK bool // /cellinfo advertised a matching OIDC issuer+audience
}

type router struct {
	inner http.Handler
	deps  Deps
	http  *http.Client

	mu      sync.Mutex
	peers   []peer
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
	// It MUST be HMAC-authenticated (ADR-0044 slice 4) — an unauthenticated
	// fanout header is a spoof or a misconfigured peer.
	if r.Header.Get(fanoutHeader) != "" {
		switch {
		case len(rt.deps.Secret) == 0:
			// No secret (single-Cell): the header can't be trusted and there
			// are no peers — strip it and serve normally (never narrow to
			// local-only on an untrusted signal).
			r.Header.Del(fanoutHeader)
		case verifyCellAuth(rt.deps.Secret, r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get(authHeader), rt.replayWindow()):
			rt.inner.ServeHTTP(w, r) // authenticated peer → local-only
			return
		default:
			http.Error(w, "cell fan-out authentication failed", http.StatusUnauthorized)
			return
		}
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
	// The federated /audit merge orders by `at` (per-Cell `seq` is not
	// comparable), so the seq-based `since` cursor is incoherent across Cells —
	// reject it LOUDLY rather than serve a silently-broken page (§1.8). Deferral:
	// a cross-Cell at-based cursor (ADR-0044).
	if r.URL.Path == "/audit" && r.URL.Query().Get("since") != "" {
		http.Error(w, "cross-Cell /audit does not support the seq-based 'since' cursor (limit-only); query one Cell directly for cursor pagination", http.StatusBadRequest)
		return
	}
	switch kind {
	case kindList:
		rt.federateList(w, r, ad, peers)
	case kindPoint:
		rt.federatePoint(w, r, peers)
	case kindAggregate:
		rt.federateAggregate(w, r, usageAdapter, peers)
	}
}

// peerSet returns the declared Cells other than self, cached briefly (so a
// single-Cell estate pays at most one ListCells query per TTL), each probed at
// /cellinfo to record whether it shares this Cell's OIDC issuer+audience (the
// discovery-time §1.6 gate).
func (rt *router) peerSet(ctx context.Context) []peer {
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
	peers := make([]peer, 0, len(cells))
	for _, c := range cells {
		if c.Name == rt.deps.CellID {
			continue
		}
		peers = append(peers, peer{name: c.Name, endpoint: c.Endpoint, issuerOK: rt.probeIssuer(ctx, c.Endpoint)})
	}
	rt.peers, rt.peersAt = peers, time.Now()
	return peers
}

// probeIssuer fetches a peer's /cellinfo and reports whether it advertises the
// same OIDC issuer+audience as this Cell. A Cell whose issuer we can't verify
// (unreachable probe or mismatch) is NOT trusted for federation — we won't
// forward a caller's token to it (§1.6/§2.5). No issuer configured locally ⇒
// skip the gate (dev/no-OIDC).
func (rt *router) probeIssuer(ctx context.Context, endpoint string) bool {
	if rt.deps.Issuer == "" {
		return true
	}
	status, body, _, err := rt.rawGet(ctx, endpoint, "/cellinfo", "")
	if err != nil || status != http.StatusOK {
		return false
	}
	var info struct{ Issuer, Audience string }
	if json.Unmarshal(body, &info) != nil {
		return false
	}
	return info.Issuer == rt.deps.Issuer && info.Audience == rt.deps.Audience
}

// federateList scatter-gathers a stable list read: serve local, fan out to
// peers concurrently (forwarding the caller's auth), merge in total order,
// truncate to limit, and write 200 (all answered) or 206 (a Cell unreachable).
func (rt *router) federateList(w http.ResponseWriter, r *http.Request, ad adapter, peers []peer) {
	rec := httptest.NewRecorder()
	rt.inner.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		copyResp(w, rec) // a local error is returned as-is, not federated
		return
	}
	bodies := [][]byte{rec.Body.Bytes()}
	queried := []string{rt.deps.CellID}
	var unreachable, skewed []string
	fwd := forwardHeaders(r)

	type res struct {
		name       string
		body       []byte
		regVersion string
		ok         bool
	}
	ch := make(chan res, len(peers))
	live := 0
	for _, p := range peers {
		queried = append(queried, p.name)
		if !p.issuerOK { // §1.6 issuer gate: never federate/forward a token to a Cell we can't verify
			skewed = append(skewed, p.name)
			continue
		}
		live++
		go func(p peer) {
			status, body, rv, err := rt.peerGet(r.Context(), p.endpoint, r.URL.Path, r.URL.RawQuery, fwd)
			ch <- res{p.name, body, rv, err == nil && status == http.StatusOK}
		}(p)
	}
	for i := 0; i < live; i++ {
		x := <-ch
		switch {
		case !x.ok:
			unreachable = append(unreachable, x.name)
		case rt.deps.RegistryVersion != "" && x.regVersion != rt.deps.RegistryVersion:
			// §1.5 registry gate: a divergent registry means same-shaped JSON
			// could be semantically skewed — NAME it, never silently union.
			skewed = append(skewed, x.name)
		default:
			bodies = append(bodies, x.body)
		}
	}

	merged, err := mergeList(bodies, ad, limitOf(r))
	if err != nil {
		// A peer body was unparseable (e.g. registry/schema skew). Return the
		// LOCAL data but NEVER a clean 200 (§1.8): name every peer skewed + 206.
		rt.deps.Log.Error("cellrouter: merge failed; returning local subset as partial", "error", err)
		all := make([]string, 0, len(peers))
		for _, p := range peers {
			all = append(all, p.name)
		}
		writePartial(w, queried, nil, all, rec.Body.Bytes())
		return
	}
	writePartial(w, queried, unreachable, skewed, merged)
}

// federatePoint routes a single-Entity read: local is authoritative if present
// (single-writer residency), else ask peers for the one Cell that homes it.
func (rt *router) federatePoint(w http.ResponseWriter, r *http.Request, peers []peer) {
	rec := httptest.NewRecorder()
	rt.inner.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		copyResp(w, rec) // local hit (200) is authoritative; other errors pass through
		return
	}
	fwd := forwardHeaders(r)
	var unreachable, skewed []string
	for _, p := range peers {
		if !p.issuerOK {
			skewed = append(skewed, p.name) // issuer-mismatched → can't trust it as authoritative
			continue
		}
		status, body, rv, err := rt.peerGet(r.Context(), p.endpoint, r.URL.Path, r.URL.RawQuery, fwd)
		switch {
		case err != nil:
			unreachable = append(unreachable, p.name)
			continue
		case rt.deps.RegistryVersion != "" && rv != "" && rv != rt.deps.RegistryVersion:
			skewed = append(skewed, p.name) // divergent registry → not authoritative
			continue
		case status == http.StatusOK:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
	}
	// If a Cell was unreachable or skewed, a 404 would be a LIE (the Entity may
	// home there), so return 503 — "could not determine existence" — never a
	// definitive not-found (§1.8). A header-ignoring REST client sees a non-2xx;
	// the MCP surface renders the message as a tool error, not "does not exist".
	if len(unreachable) > 0 || len(skewed) > 0 {
		note := "entity not found in reachable Cells"
		if len(unreachable) > 0 {
			sort.Strings(unreachable)
			w.Header().Set(hdrUnreachable, strings.Join(unreachable, ","))
			note += "; unreachable: " + strings.Join(unreachable, ",")
		}
		if len(skewed) > 0 {
			sort.Strings(skewed)
			w.Header().Set(hdrSkewed, strings.Join(skewed, ","))
			note += "; skewed: " + strings.Join(skewed, ",")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"` + note + ` — it may reside there"}`))
		return
	}
	// Genuinely absent from every reachable Cell, none unreachable → honest 404.
	copyResp(w, rec)
}

// federateAggregate scatter-gathers a per-identity aggregate (/usage) and
// group-SUM/MAX-merges it (no truncation). Same issuer/registry skew gates and
// partial-result honesty as a list read.
func (rt *router) federateAggregate(w http.ResponseWriter, r *http.Request, agg aggAdapter, peers []peer) {
	rec := httptest.NewRecorder()
	rt.inner.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		copyResp(w, rec)
		return
	}
	bodies := [][]byte{rec.Body.Bytes()}
	queried := []string{rt.deps.CellID}
	var unreachable, skewed []string
	fwd := forwardHeaders(r)

	type res struct {
		name       string
		body       []byte
		regVersion string
		ok         bool
	}
	ch := make(chan res, len(peers))
	live := 0
	for _, p := range peers {
		queried = append(queried, p.name)
		if !p.issuerOK {
			skewed = append(skewed, p.name)
			continue
		}
		live++
		go func(p peer) {
			status, body, rv, err := rt.peerGet(r.Context(), p.endpoint, r.URL.Path, r.URL.RawQuery, fwd)
			ch <- res{p.name, body, rv, err == nil && status == http.StatusOK}
		}(p)
	}
	for i := 0; i < live; i++ {
		x := <-ch
		switch {
		case !x.ok:
			unreachable = append(unreachable, x.name)
		case rt.deps.RegistryVersion != "" && x.regVersion != rt.deps.RegistryVersion:
			skewed = append(skewed, x.name)
		default:
			bodies = append(bodies, x.body)
		}
	}
	merged, err := mergeAggregate(bodies, agg)
	if err != nil {
		rt.deps.Log.Error("cellrouter: aggregate merge failed; returning local subset as partial", "error", err)
		all := make([]string, 0, len(peers))
		for _, p := range peers {
			all = append(all, p.name)
		}
		writePartial(w, queried, nil, all, rec.Body.Bytes())
		return
	}
	writePartial(w, queried, unreachable, skewed, merged)
}

func writePartial(w http.ResponseWriter, queried, unreachable, skewed []string, body []byte) {
	sort.Strings(queried)
	w.Header().Set(hdrQueried, strings.Join(queried, ","))
	code := http.StatusOK
	if len(unreachable) > 0 {
		sort.Strings(unreachable)
		w.Header().Set(hdrUnreachable, strings.Join(unreachable, ","))
		code = http.StatusPartialContent
	}
	if len(skewed) > 0 {
		sort.Strings(skewed)
		w.Header().Set(hdrSkewed, strings.Join(skewed, ","))
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

func (rt *router) replayWindow() time.Duration {
	if rt.deps.ReplayWindow > 0 {
		return rt.deps.ReplayWindow
	}
	return defaultReplayWindow
}
