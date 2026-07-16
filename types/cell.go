package types

import (
	"regexp"
	"strings"
)

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

// CellScopeToken returns the NATS subject/stream scope token for a Cell — the
// one string both strattd (hub) and stratt-agent derive, IDENTICALLY, from
// shared env so the two ends always exchange on the same subjects (ADR-0044
// slice 6). It is "" for the built-in LocalCell (so a single-Cell deployment's
// stream/subject names stay byte-identical to the pre-Cells control plane), the
// override when a Cell declares a DispatchPrefix (graph.cell.dispatch_prefix /
// STRATT_CELL_DISPATCH_PREFIX), else the Cell name.
//
// The agent has no database, so the runtime token can only come from env — the
// hub reconciles its env-derived token against its CaC-declared DispatchPrefix
// at boot and loud-fails on a mismatch (§2.4 exactly-one-answer; no silent
// precedence between the deployed env and the declared desired state).
func CellScopeToken(cell, override string) string {
	if override != "" {
		return override
	}
	if cell == "" || cell == LocalCell {
		return ""
	}
	return cell
}

// cellScopeTokenRE constrains a scope token to NATS-subject-safe and
// JetStream-name-safe characters: lower-case alphanumerics and '-', starting
// alphanumeric. It deliberately EXCLUDES '.' (which would inject an extra NATS
// subject token and silently reshape the topology — stratt.a.b.run.), the NATS
// wildcards '>' and '*', and whitespace.
var cellScopeTokenRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidCellScopeToken reports whether tok may be embedded in a NATS subject
// token and a JetStream stream name (ADR-0044 slice 6). The empty token
// (LocalCell — the byte-identical no-op) is always valid; any other token must
// match cellScopeTokenRE, so a stray '.' or wildcard in a Cell name or
// STRATT_CELL_DISPATCH_PREFIX is caught loudly (at CaC compile and at boot)
// rather than silently corrupting the subject topology.
func ValidCellScopeToken(tok string) bool {
	return tok == "" || cellScopeTokenRE.MatchString(tok)
}

// ScopedStream cell-scopes a JetStream stream/KV base name: "STRATT_DISPATCH"
// stays itself for local (tok=="") and becomes "STRATT_DISPATCH_<TOK>" for a
// named Cell. Uppercased so the derived name is a legal JetStream name.
func ScopedStream(base, tok string) string {
	if tok == "" {
		return base
	}
	return base + "_" + strings.ToUpper(tok)
}

// ScopedSubjectRoot cell-scopes a NATS subject root by inserting the token as
// the second subject token: "stratt.run." stays itself for local and becomes
// "stratt.<tok>.run." for a named Cell. root MUST begin with "stratt.".
func ScopedSubjectRoot(root, tok string) string {
	if tok == "" {
		return root
	}
	return "stratt." + tok + "." + strings.TrimPrefix(root, "stratt.")
}
