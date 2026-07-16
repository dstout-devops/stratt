// Package saltsim is a dev-harness stand-in for salt-api (the graphsim/chefsim/
// puppetdbsim posture, §1.5 — a test double; the sovereign contract is the
// plugin's, never this). It serves just enough of the rest_cherrypy API for the
// salt Syncer plugin to run its real code paths with no real Salt master —
// eauth login + a token gate and the runner cache.grains enumeration. The reason
// harnesses are first-class for this out-of-network OSS build.
//
// Syncer-only: the /events SSE stream the in-tree Emitter uses is deliberately
// omitted here — the Emitter is not part of this Phase-C extraction.
package saltsim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// simToken is the single token this sim issues at /login and requires on every
// authenticated call — enough to prove the eauth token flow end to end.
const simToken = "sim-token"

// Sim is the in-memory fixture service.
type Sim struct {
	mu     sync.Mutex
	grains map[string]map[string]any // minion id -> grains
}

// New returns an empty Sim. Seed minions with SetMinion.
func New() *Sim {
	return &Sim{grains: map[string]map[string]any{}}
}

// SetMinion adds or replaces a minion's grains (also via POST /_sim/minions).
func (s *Sim) SetMinion(id string, grains map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grains[id] = grains
}

// RemoveMinion drops a minion (also via POST /_sim/remove).
func (s *Sim) RemoveMinion(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.grains, id)
}

// Handler serves login, the runner enumeration, and the seeding hooks.
func (s *Sim) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /", s.run)
	mux.HandleFunc("POST /_sim/minions", s.simSetMinion)
	mux.HandleFunc("POST /_sim/remove", s.simRemove)
	return mux
}

func (s *Sim) login(w http.ResponseWriter, r *http.Request) {
	var body struct{ Username, Password, Eauth string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Username == "" {
		http.Error(w, `{"error":"bad login"}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("X-Auth-Token", simToken)
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"return":[{"token":%q,"user":%q,"eauth":%q}]}`, simToken, body.Username, body.Eauth)
}

func (s *Sim) authed(r *http.Request) bool {
	return r.Header.Get("X-Auth-Token") == simToken
}

// run handles the runner lowstate; only cache.grains is served.
func (s *Sim) run(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	var low struct {
		Client string `json:"client"`
		Fun    string `json:"fun"`
		Tgt    string `json:"tgt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&low); err != nil {
		http.Error(w, `{"error":"bad lowstate"}`, http.StatusBadRequest)
		return
	}
	if low.Client != "runner" || low.Fun != "cache.grains" {
		http.Error(w, `{"error":"unsupported fun"}`, http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	out := map[string]map[string]any{}
	for id, g := range s.grains {
		out[id] = g
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"return": []any{out}})
}

func (s *Sim) simSetMinion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string         `json:"id"`
		Grains map[string]any `json:"grains"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	s.SetMinion(body.ID, body.Grains)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Sim) simRemove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	s.RemoveMinion(body.ID)
	w.WriteHeader(http.StatusNoContent)
}
