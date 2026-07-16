package types

// Cell is a region-local, single-writer control-plane shard — its own
// boring-spine substrate (Postgres/NATS/Temporal/OpenFGA/object-store), region-
// local HA with cross-region DR (charter §2.3 Named Kind; ADR-0044). The fleet
// is many Cells presenting one logical estate, active/active across Cells with
// NO datum multi-master: every datum has exactly one home Cell that is its sole
// writer (§2.1). A Cell contains Sites; the built-in default is one Cell
// (LocalCell). A Cell is a CaC-declared projection (§1.2): the declaration lives
// in Git; the sole writer is the desired-state engine, mirroring Site.
type Cell struct {
	// Name is the stable Cell id, stamped into provenance (prov_cell), the
	// leader lease, Temporal namespace/queue, and NATS subject prefixes. Never
	// "local" — reserved for the built-in single-Cell default.
	Name string `json:"name"`
	// Region is the deployment region this Cell lives in (one Cell maps to one
	// region; multiple Cells may share a region for tenant/blast-radius
	// partitioning).
	Region string `json:"region"`
	// Endpoint is the Cell's strattd API address — the federation router fans
	// reads out to, and forwards home-Cell writes to, peer Endpoints (ADR-0044).
	Endpoint string `json:"endpoint"`
	// DispatchPrefix optionally overrides the NATS subject/stream prefix for
	// this Cell (defaults derive from Name).
	DispatchPrefix string `json:"dispatchPrefix,omitempty"`
	// Description is free-form operator context.
	Description string `json:"description,omitempty"`
	// DeclaredBy records the declaration path: "cac" (Git desired state) or
	// "api". Mirrors Site/View/Trigger/Emitter.
	DeclaredBy string `json:"declaredBy,omitempty"`
	// AuthzHome designates the ONE Cell whose leader syncs the global OpenFGA
	// tuple store (ADR-0044 slice 4). Exactly one Cell in a named fleet carries
	// it; every other Cell reads authz but never writes tuples, so N Cells
	// sharing one OpenFGA cannot thrash each other's grants (SyncTuples is an
	// authoritative single-writer). Validated exactly-one at CaC compile.
	AuthzHome bool `json:"authzHome,omitempty"`
}

// LocalCell is the built-in, single-Cell default (today's deployment, dev, and
// compose). A datum with no declared Cell homes here; this Cell is never
// declared, never a graph.cell row, and its identity is stamped without the
// collision-safe prefixes a named Cell uses — so a single-Cell deployment is
// byte-identical to the pre-Cells control plane.
const LocalCell = "local"
