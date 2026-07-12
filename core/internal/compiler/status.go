package compiler

import (
	"sync"
	"time"
)

// Status holds the most recent compile pass for the read-only GET /compile
// surface — the §4.3 "stratt plan renders membership deltas" view, plus any
// compile errors and max-delta pauses (§1.8: the wait is visible).
type Status struct {
	mu   sync.Mutex
	snap Snapshot
}

// Snapshot is one compile pass's reportable outcome.
type Snapshot struct {
	CompiledAt        time.Time         `json:"compiledAt"`
	CompiledBaselines int               `json:"compiledBaselines"`
	Errors            []string          `json:"errors,omitempty"`
	Deltas            []AssignmentDelta `json:"deltas,omitempty"`
}

// Set records the latest pass.
func (s *Status) Set(snap Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = snap
}

// Get returns the latest pass (zero value before the first compile).
func (s *Status) Get() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap
}
