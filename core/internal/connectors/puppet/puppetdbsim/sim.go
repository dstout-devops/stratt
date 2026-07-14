// Package puppetdbsim is a dev-harness stand-in for the PuppetDB/OpenVoxDB v4
// query API (the graphsim/chefsim posture, §1.5 — a test double; the sovereign
// contract is the Syncer's, never this). It serves the /pdb/query/v4/inventory
// endpoint with real pagination (limit/offset + X-Records) so the puppet Syncer
// runs its real enumeration/normalize/project code paths with no real server —
// the reason harnesses are first-class for this out-of-network OSS build.
//
// It serves plain HTTP, PuppetDB's legitimate localhost dev listener (:8080);
// the mTLS client path is proven separately in the connector's auth test.
package puppetdbsim

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Node is one /inventory entry — the subset the Syncer consumes. Facts holds
// the Facter structured facts; Trusted holds CA-derived identity.
type Node struct {
	Certname    string         `json:"certname"`
	Timestamp   string         `json:"timestamp,omitempty"`
	Environment string         `json:"environment,omitempty"`
	Facts       map[string]any `json:"facts,omitempty"`
	Trusted     map[string]any `json:"trusted,omitempty"`
}

// Sim is the in-memory fixture service.
type Sim struct {
	mu    sync.Mutex
	nodes map[string]Node
}

// New returns an empty Sim. Seed nodes with Set.
func New() *Sim { return &Sim{nodes: map[string]Node{}} }

// Set adds or replaces a node (also usable via POST /_sim/nodes).
func (s *Sim) Set(n Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[n.Certname] = n
}

// Remove deletes a node (also usable via POST /_sim/remove).
func (s *Sim) Remove(certname string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.nodes, certname)
}

// Handler serves the inventory endpoint plus unauthenticated test hooks.
func (s *Sim) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /pdb/query/v4/inventory", s.inventory)
	mux.HandleFunc("POST /_sim/nodes", s.simSet)
	mux.HandleFunc("POST /_sim/remove", s.simRemove)
	return mux
}

// inventory returns active nodes with their full fact set, sorted by certname,
// honoring limit/offset; include_total=true sets the X-Records grand total
// header (the real paging contract).
func (s *Sim) inventory(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	all := make([]Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		all = append(all, n)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Certname < all[j].Certname })

	q := r.URL.Query()
	if strings.EqualFold(q.Get("include_total"), "true") {
		w.Header().Set("X-Records", strconv.Itoa(len(all)))
	}
	offset := atoiDefault(q.Get("offset"), 0)
	limit := atoiDefault(q.Get("limit"), len(all))
	page := paginate(all, offset, limit)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

func (s *Sim) simSet(w http.ResponseWriter, r *http.Request) {
	var n Node
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil || n.Certname == "" {
		http.Error(w, "node.certname required", http.StatusBadRequest)
		return
	}
	s.Set(n)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Sim) simRemove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Certname string `json:"certname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Certname == "" {
		http.Error(w, "certname required", http.StatusBadRequest)
		return
	}
	s.Remove(body.Certname)
	w.WriteHeader(http.StatusNoContent)
}

func paginate(all []Node, offset, limit int) []Node {
	if offset >= len(all) {
		return []Node{}
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end]
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}
