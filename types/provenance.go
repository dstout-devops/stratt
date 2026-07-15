package types

import "time"

// WriterKind enumerates the only two legal write paths into the graph
// (charter §1.2): a Normalizer projecting a Syncer's observations, or Run
// provenance written by an execution. This is enforced in the data layer,
// not by convention.
type WriterKind string

const (
	WriterSyncer WriterKind = "syncer"
	WriterRun    WriterKind = "run"
)

// Provenance is the per-attribute stamp: which Run or Syncer wrote the value,
// when, and from which Source (charter §2.1). It is the audit story and the
// "why is this value here" answer — which always has exactly one answer.
type Provenance struct {
	WriterKind WriterKind `json:"writerKind"`
	// WriterRef identifies the writer: a Syncer id (Connector@version/syncer)
	// or a Run id.
	WriterRef string `json:"writerRef"`
	// SourceID is the external system of record the value was observed from,
	// empty for Run-written facts whose source is the execution itself.
	SourceID string `json:"sourceId,omitempty"`
	// Cell is which control-plane Cell wrote the value (ADR-0044), stamped by
	// the writing Store from STRATT_CELL_ID. Empty/"local" for the single-Cell
	// default. Read-back only — the write stamp is the daemon's own Cell, not a
	// caller choice (which Cell wrote this has exactly one answer, §2.1).
	Cell string    `json:"cell,omitempty"`
	At   time.Time `json:"at"`
}
