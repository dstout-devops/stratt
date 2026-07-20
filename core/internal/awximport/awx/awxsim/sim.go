// Package awxsim is a dev/test stand-in for an AWX 24.6.1 /api/v2 surface (the
// graphsim/vcsim posture, ADR-0014): just enough of the REST protocol —
// bearer auth, the {count,next,previous,results} envelope with real paging, and
// the sub-resources (survey_spec, projects, workflow_nodes, inventory_sources,
// hosts) — for the awx read client to run its real code paths. Never shipped,
// never load-bearing (§1.5).
package awxsim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const pageSize = 2 // small on purpose: multi-item collections exercise next

// Sim is the in-memory fixture. Data comes from Seed (fixtures.go); the mux is
// read-only, mirroring the importer's read-only client.
type Sim struct {
	data *estate
	base string
}

// New returns a Sim seeded with the canned migration estate. base is the URL
// clients reach it at (used to mint absolute next links, like real AWX).
func New(base string) *Sim {
	return &Sim{data: seed(), base: strings.TrimRight(base, "/")}
}

// SetBase updates the link base (httptest servers learn their URL late).
func (s *Sim) SetBase(base string) { s.base = strings.TrimRight(base, "/") }

// Handler serves the subset of /api/v2 the importer reads.
func (s *Sim) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/ping/", s.auth(s.ping))
	mux.HandleFunc("GET /api/v2/job_templates/", s.auth(s.jobTemplates))
	mux.HandleFunc("GET /api/v2/job_templates/{id}/", s.auth(s.jobTemplate))
	mux.HandleFunc("GET /api/v2/job_templates/{id}/survey_spec/", s.auth(s.surveySpec))
	mux.HandleFunc("GET /api/v2/projects/{id}/", s.auth(s.project))
	mux.HandleFunc("GET /api/v2/workflow_job_templates/", s.auth(s.workflowJTs))
	mux.HandleFunc("GET /api/v2/workflow_job_templates/{id}/workflow_nodes/", s.auth(s.workflowNodes))
	mux.HandleFunc("GET /api/v2/inventories/", s.auth(s.inventories))
	mux.HandleFunc("GET /api/v2/inventories/{id}/", s.auth(s.inventory))
	mux.HandleFunc("GET /api/v2/inventories/{id}/inventory_sources/", s.auth(s.inventorySources))
	mux.HandleFunc("GET /api/v2/inventories/{id}/hosts/", s.auth(s.hosts))
	mux.HandleFunc("GET /api/v2/credentials/", s.auth(s.credentials))
	mux.HandleFunc("GET /api/v2/credentials/{id}/", s.auth(s.credential))
	return mux
}

// auth rejects any request without a bearer token (the importer always sends
// one; the 401 path is exercised by omitting it).
func (s *Sim) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, `{"detail":"Authentication credentials were not provided."}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Sim) ping(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"version": "24.6.1"})
}

// paged renders a collection as an AWX list envelope for the requested page,
// minting an absolute next link for the following page.
func paged[T any](s *Sim, w http.ResponseWriter, r *http.Request, path string, items []T) {
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			page = n
		}
	}
	start := (page - 1) * pageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	next := ""
	if end < len(items) {
		next = fmt.Sprintf("%s%s?page=%d", s.base, path, page+1)
	}
	writeJSON(w, map[string]any{
		"count":    len(items),
		"next":     next,
		"previous": nil,
		"results":  items[start:end],
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
