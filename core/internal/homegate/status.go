package homegate

import "sync"

// SourceRuntime is this daemon's live view of a Source it is configured for
// (ADR-0045 must-fix 4): whether it is actively projecting it, standing by for a
// re-home, or blocked — so an operator can tell "standby (expected)" from
// "broken". Surfaced on the sources read model (§1.6, GET /sources).
type SourceRuntime struct {
	// State is Active / Standby / Sealed / Greenfield / Uncertain.
	State HomeState `json:"state"`
	// HomeCell is the Cell that homes the Source (this daemon for Active/Sealed,
	// the peer for Standby), empty for Greenfield/Uncertain.
	HomeCell string `json:"homeCell,omitempty"`
}

// Status is the in-memory map of per-Source runtime state the supervisor writes
// and the API reads (mirror of the Site-liveness pattern — runtime state never
// goes in the graph, §1.2). Concurrency-safe.
type Status struct {
	mu sync.RWMutex
	m  map[string]SourceRuntime
}

// NewStatus returns an empty Status.
func NewStatus() *Status { return &Status{m: map[string]SourceRuntime{}} }

// Set records a Source's current runtime state.
func (s *Status) Set(source string, h Home) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[source] = SourceRuntime{State: h.State, HomeCell: h.Cell}
}

// Snapshot returns a copy of the current per-Source runtime state.
func (s *Status) Snapshot() map[string]SourceRuntime {
	out := map[string]SourceRuntime{}
	if s == nil {
		return out
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for k, v := range s.m {
		out[k] = v
	}
	return out
}
