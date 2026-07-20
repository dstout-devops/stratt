// Package adopt implements the TOOL-BLIND half of `stratt adopt` (ADR-0086/0088/0089): taking
// an ALREADY-OBSERVED foreign object and resolving the coordinates a plugin needs to materialize
// it as a reviewable, Git-declared Stratt Named Kind — per-object, in-place, over the live
// projection. We never import: the projection is the always-on catalog; adopt resolves ONE
// object from it (a graph read only). The credential-bearing deep-read + the AWX→Named-Kind
// TRANSFORM are tool-specific breadth and live in the awx plugin (ADR-0089); this package names
// no tool. It writes NO graph state (§1.2); the emitted bundle is the Run's output, reviewed and
// merged, Gated like any other (§5, no auto-launch).
package adopt

import (
	"context"
	"errors"
	"fmt"
)

// KindTemplate is the kind adopt resolves today (an AWX job template → a single-Step Workflow +
// its View, materialized plugin-side). Workflow-job-template and other kinds follow the same shape.
const KindTemplate = "ansible.template"

// Client-error sentinels so a transport (the API) can map them to precise status codes (400/404)
// rather than a blanket 500. The messages keep the human phrasing.
var (
	// ErrUnsupportedKind: a kind adopt cannot resolve yet.
	ErrUnsupportedKind = errors.New("unsupported kind")
	// ErrNotObserved: the object is not in the projection catalog — nothing to adopt.
	ErrNotObserved = errors.New("not in the projection catalog")
)

// Catalog is the live projection — the always-on record of WHAT we already observe. adopt
// resolves the object here FIRST: you can only adopt what is projected (fail-loud otherwise),
// and the catalog yields the native (foreign-system) object id the plugin's deep-read is keyed on.
type Catalog interface {
	// Resolve reports whether an object of kind+identity is currently projected, its native
	// object id, and the source that projects it. found=false ⇒ not observed, nothing to adopt.
	Resolve(ctx context.Context, kind, identity string) (nativeID int, source string, found bool, err error)
	// LiveExecutions returns the NAMES of foreign-side executions still ENABLED that target this
	// object (e.g. AWX schedules that launch the template) — the double-execution the operator
	// must disable at cutover (ADR-0086 §4). Advisory/best-effort: read from the same projection
	// catalog, it never blocks the adopt.
	LiveExecutions(ctx context.Context, kind, identity string) ([]string, error)
}

// Request names the object to adopt.
type Request struct {
	Kind     string // e.g. ansible.template
	Identity string // the projection identity, e.g. "ctrl-a/10"
}

// Resolved is the credential-free result of Resolve: the coordinates the plugin-side transform
// needs. It carries NO material and NO graph handle — it crosses to the awx plugin as the
// adopt/materialize Action's input (ADR-0088/0089). NativeID keys the deep-read; Source stamps
// the lineage; Live is the pre-resolved cutover guard set (the plugin never touches the catalog).
type Resolved struct {
	NativeID int
	Source   string
	Live     []string
}

// Resolve is the tool-blind core half of adopt (ADR-0088/0089): it reads ONLY the graph —
// resolve the object from the projection catalog (fail-loud if not observed, §7.6) and
// best-effort enumerate the still-live foreign-side executions for the cutover guard. No
// credential, no tool I/O — this is the spine part that legitimately runs in the control plane,
// and its result crosses to the awx-plugin Action as opaque coordinates.
func Resolve(ctx context.Context, cat Catalog, req Request) (Resolved, error) {
	if req.Kind != KindTemplate {
		return Resolved{}, fmt.Errorf("adopt: %w %q (supported: %s)", ErrUnsupportedKind, req.Kind, KindTemplate)
	}
	nativeID, source, found, err := cat.Resolve(ctx, req.Kind, req.Identity)
	if err != nil {
		return Resolved{}, fmt.Errorf("adopt: catalog resolve %s %q: %w", req.Kind, req.Identity, err)
	}
	if !found {
		// The strangler cutover only adopts what we already observe (§7.6).
		return Resolved{}, fmt.Errorf("adopt: %s %q is %w — nothing to adopt (is its Connector syncing?)", req.Kind, req.Identity, ErrNotObserved)
	}
	// Advisory (ADR-0086 §4): a lookup error must not fail the adopt — a nil Live degrades to a
	// "verify manually" note in the guard, never a blocked cutover.
	live, _ := cat.LiveExecutions(ctx, req.Kind, req.Identity)
	return Resolved{NativeID: nativeID, Source: source, Live: live}, nil
}
