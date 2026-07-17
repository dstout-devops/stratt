// Package graphsim is a dev-harness stand-in for the Microsoft Graph device
// API (the vcsim posture, ADR-0014): just enough OAuth + /devices/delta
// protocol for the msgraph Syncer to run its real code paths — paging, delta
// tokens, removals, and token expiry (410). Never shipped; never load-bearing
// (§1.5 — the sovereign contract is the Syncer's, this is a test double).
package graphsim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const pageSize = 2 // small on purpose: full enumerations exercise nextLink

type device struct {
	ID                     string `json:"id"`
	DeviceID               string `json:"deviceId,omitempty"`
	DisplayName            string `json:"displayName,omitempty"`
	OperatingSystem        string `json:"operatingSystem,omitempty"`
	OperatingSystemVersion string `json:"operatingSystemVersion,omitempty"`
	AccountEnabled         *bool  `json:"accountEnabled,omitempty"`
	TrustType              string `json:"trustType,omitempty"`
	ProfileType            string `json:"profileType,omitempty"`
}

type entry struct {
	dev     device
	version int
	removed bool
}

// Sim is the in-memory fixture service.
type Sim struct {
	mu      sync.Mutex
	entries map[string]*entry
	version int
	// minToken: delta tokens older than this return 410 (POST /_sim/expire).
	minToken int
	base     string // externally visible base URL for next/delta links
}

// New returns a Sim; call Handler for its mux. base is the URL clients reach
// it at (used to mint absolute nextLink/deltaLink, like the real service).
func New(base string) *Sim {
	return &Sim{entries: map[string]*entry{}, base: strings.TrimRight(base, "/")}
}

// SetBase updates the link base (httptest servers learn their URL late).
func (s *Sim) SetBase(base string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.base = strings.TrimRight(base, "/")
}

// Handler serves the token endpoint, the delta API, and the mutation hooks.
func (s *Sim) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", s.token)
	mux.HandleFunc("GET /v1.0/devices/delta", s.delta)
	mux.HandleFunc("POST /_sim/devices", s.mutate)
	mux.HandleFunc("POST /_sim/expire", s.expire)
	return mux
}

func (s *Sim) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "client_credentials" {
		http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, `{"access_token":"sim-token","token_type":"Bearer","expires_in":3600}`)
}

func (s *Sim) delta(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		http.Error(w, `{"error":{"code":"InvalidAuthenticationToken"}}`, http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	q := r.URL.Query()
	out := map[string]any{}

	if tok := q.Get("$deltatoken"); tok != "" {
		since, err := strconv.Atoi(tok)
		if err != nil || since < s.minToken {
			http.Error(w, `{"error":{"code":"syncStateNotFound"}}`, http.StatusGone)
			return
		}
		changes := []any{}
		for _, e := range s.sorted() {
			if e.version <= since {
				continue
			}
			if e.removed {
				changes = append(changes, map[string]any{
					"id": e.dev.ID, "@removed": map[string]string{"reason": "deleted"},
				})
			} else {
				changes = append(changes, e.dev)
			}
		}
		out["value"] = changes
		out["@odata.deltaLink"] = fmt.Sprintf("%s/v1.0/devices/delta?$deltatoken=%d", s.base, s.version)
	} else {
		// Full enumeration, paged, live devices only.
		offset := 0
		if st := q.Get("$skiptoken"); st != "" {
			offset, _ = strconv.Atoi(st)
		}
		live := []device{}
		for _, e := range s.sorted() {
			if !e.removed {
				live = append(live, e.dev)
			}
		}
		end := min(offset+pageSize, len(live))
		out["value"] = live[offset:end]
		if end < len(live) {
			out["@odata.nextLink"] = fmt.Sprintf("%s/v1.0/devices/delta?$skiptoken=%d", s.base, end)
		} else {
			out["@odata.deltaLink"] = fmt.Sprintf("%s/v1.0/devices/delta?$deltatoken=%d", s.base, s.version)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// mutate applies {op: add|update|remove, device: {...}} and bumps the version.
func (s *Sim) mutate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Op     string `json:"op"`
		Device device `json:"device"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Device.ID == "" {
		http.Error(w, "op and device.id required", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version++
	switch body.Op {
	case "add", "update":
		s.entries[body.Device.ID] = &entry{dev: body.Device, version: s.version}
	case "remove":
		if e, ok := s.entries[body.Device.ID]; ok {
			e.removed = true
			e.version = s.version
		}
	default:
		http.Error(w, "op must be add, update, or remove", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// expire invalidates every outstanding delta token (forces 410 → resync).
func (s *Sim) expire(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.minToken = s.version + 1
	s.version++
	w.WriteHeader(http.StatusNoContent)
}

func (s *Sim) sorted() []*entry {
	out := make([]*entry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].dev.ID < out[j].dev.ID })
	return out
}
