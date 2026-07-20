// Package adopt implements `stratt adopt` (ADR-0086): taking an ALREADY-OBSERVED foreign
// object and materializing it as a reviewable, Git-declared Stratt Named Kind — per-object,
// in-place, over the live projection. We never import: the projection is the always-on
// catalog; adopt resolves ONE object from it, does a targeted read-only deep-read of just
// that object's definition (model (b)), and runs the (kept) awximport transform. It writes
// desired state to a bundle — never the projection graph (§1.2); the operator reviews and
// merges; the adopted Workflow is Gated like any other (§5, no auto-launch).
//
// This package is transport-agnostic: the CLI is the first client, the API (§1.6) the next.
package adopt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dstout-devops/stratt/core/internal/awximport"
	"github.com/dstout-devops/stratt/core/internal/awximport/awx"
)

// KindTemplate is the kind adopt materializes today (an AWX job template → a single-Step
// Workflow + its View). Workflow-job-template and other kinds follow the same shape.
const KindTemplate = "ansible.template"

// Client-error sentinels so a transport (the API) can map them to precise status codes
// (400/404) rather than a blanket 500. The messages keep the human phrasing.
var (
	// ErrUnsupportedKind: a kind adopt cannot transform yet.
	ErrUnsupportedKind = errors.New("unsupported kind")
	// ErrNotObserved: the object is not in the projection catalog — nothing to adopt.
	ErrNotObserved = errors.New("not in the projection catalog")
	// ErrGoneAtRead: the object vanished between catalog and deep-read — never emit stale CaC.
	ErrGoneAtRead = errors.New("gone at read-time")
)

// Catalog is the live projection — the always-on record of WHAT we already observe. adopt
// resolves the object here FIRST: you can only adopt what is projected (fail-loud otherwise),
// and the catalog yields the native (foreign-system) object id the deep-read is keyed on.
type Catalog interface {
	// Resolve reports whether an object of kind+identity is currently projected, its native
	// object id, and the source that projects it. found=false ⇒ not observed, nothing to adopt.
	Resolve(ctx context.Context, kind, identity string) (nativeID int, source string, found bool, err error)
	// LiveExecutions returns the NAMES of foreign-side executions still ENABLED that target
	// this object (e.g. AWX schedules that launch the template) — the double-execution the
	// operator must disable at cutover (ADR-0086 §4). Advisory/best-effort: read from the
	// same projection catalog, it never blocks the adopt.
	LiveExecutions(ctx context.Context, kind, identity string) ([]string, error)
}

// Reader is the targeted, read-only deep-reader for one object's full definition (model (b)).
// *awx.Client satisfies it; a fake satisfies it in tests.
type Reader interface {
	ReadJobTemplate(ctx context.Context, nativeID int) (*awx.Snapshot, error)
}

// Request names the object to adopt.
type Request struct {
	Kind     string // e.g. ansible.template
	Identity string // the projection identity, e.g. "ctrl-a/10"
}

// Resolved is the core-side, credential-free result of Resolve: the coordinates the pod-side
// Materialize needs. It carries NO material and NO graph handle — it crosses the pod boundary
// as the adopt/materialize Action's input (ADR-0088). NativeID keys the deep-read; Source
// stamps the lineage; Live is the pre-resolved cutover guard set (the pod never touches the
// catalog).
type Resolved struct {
	NativeID int
	Source   string
	Live     []string
}

// Resolve is the core-side half of adopt (ADR-0088): it reads ONLY the graph — resolve the
// object from the projection catalog (fail-loud if not observed, §7.6) and best-effort
// enumerate the still-live foreign-side executions for the cutover guard. No credential, no
// AWX I/O — this is the part that legitimately runs in the control plane, and its result
// crosses to the Materialize pod as opaque coordinates.
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
	// Advisory (ADR-0086 §4): a lookup error must not fail the adopt — a nil Live degrades to
	// a "verify manually" note in the guard, never a blocked cutover.
	live, _ := cat.LiveExecutions(ctx, req.Kind, req.Identity)
	return Resolved{NativeID: nativeID, Source: source, Live: live}, nil
}

// Materialize is the pod-side half of adopt (ADR-0088): given a Reader (an AWX client built
// from the pod-resolved token) and the already-resolved coordinates, it does the targeted
// read-only deep-read (definition-truth; fail-loud if gone at read-time), runs the kept
// awximport transform, stamps adopted-from lineage, and appends the cutover guard from the
// pre-resolved Live set. It touches NO graph — its only inputs are the Reader and Resolved.
func Materialize(ctx context.Context, reader Reader, req Request, resolved Resolved) (*awximport.Emit, error) {
	// The LIVE targeted read is definition-truth, never the possibly-stale catalog.
	snap, err := reader.ReadJobTemplate(ctx, resolved.NativeID)
	if err != nil {
		return nil, fmt.Errorf("adopt: deep-read %s native %d: %w", req.Kind, resolved.NativeID, err)
	}
	if len(snap.JobTemplates) == 0 {
		return nil, fmt.Errorf("adopt: %s %q (native %d) is %w — not adopting stale state", req.Kind, req.Identity, resolved.NativeID, ErrGoneAtRead)
	}

	emit, err := awximport.Bundle(snap, awximport.Options{
		// Structured adopt lineage on the emitted Workflow (ADR-0087) — what the standing
		// cutover reconciler reads. Lives on the Named Kind's Git lineage, never a projection facet.
		AdoptedFrom: &awximport.AdoptLineage{Kind: req.Kind, Identity: req.Identity, Source: resolved.Source},
	})
	if err != nil {
		return nil, err
	}
	stampLineage(emit, req, resolved.NativeID, resolved.Source, time.Now().UTC())
	cutoverGuard(emit, resolved.Live)
	return emit, nil
}

// Adopt composes Resolve ∘ Materialize in one process (the in-tree convenience used by unit
// tests and any in-process caller). Production splits the two across the pod boundary
// (ADR-0088): Resolve in the API server, Materialize in the stratt-adopt pod. It never writes
// the projection graph; not-in-catalog and gone-at-read-time both fail loud (§1.8).
func Adopt(ctx context.Context, cat Catalog, reader Reader, req Request) (*awximport.Emit, error) {
	resolved, err := Resolve(ctx, cat, req)
	if err != nil {
		return nil, err
	}
	return Materialize(ctx, reader, req, resolved)
}

// cutoverGuard appends the ADR-0086 §4 anti-double-execution section to the report: once the
// bundle is merged, Stratt owns execution, so any still-live foreign-side execution (an
// enabled AWX schedule) must be disabled or the object runs in BOTH places. The live set was
// pre-resolved core-side by Resolve (§1.2 graph read) and handed in — making the cutover
// explicit and diagnosable (§1.8), never a silent dual truth. A nil set degrades to a
// "verify manually" note, never a failed adopt.
func cutoverGuard(emit *awximport.Emit, live []string) {
	var b strings.Builder
	b.WriteString("\n## Cutover — avoid double-execution (ADR-0086 §4)\n")
	b.WriteString("On merge, Stratt owns execution of this object. Disable its foreign-side execution so it does not run in both places.\n")
	switch len(live) {
	case 0:
		b.WriteString("\nNo enabled AWX schedules were resolved for it. Still verify no manual or other launch remains.\n")
	default:
		b.WriteString("\nDISABLE these still-enabled AWX schedules (they currently launch this object):\n")
		for _, name := range live {
			b.WriteString("  - " + name + "\n")
		}
	}
	emit.Report += b.String()
}

// stampLineage records the adopted-from linkage back to the source object (must-fix 2 —
// §1.8 descent / audit) as the leading provenance comment of each emitted Named-Kind file,
// replacing the retired "generated by import" banner (ADR-0086). Structured provenance
// labels on the Named Kind follow as the desired-state schema gains them; the comment is the
// durable lineage today and rides through desiredstate's KnownFields round-trip untouched.
// The lineage is also prepended to the report. JSON contracts (no comment syntax) carry
// lineage via their referencing Workflow + the report, not a header line.
func stampLineage(emit *awximport.Emit, req Request, nativeID int, source string, at time.Time) {
	banner := fmt.Sprintf("# adopted-from: %s %s (native %s/%d) at %s (ADR-0086) — review before merge.\n",
		source, req.Identity, req.Kind, nativeID, at.Format(time.RFC3339))
	for path, content := range emit.Files {
		if !strings.HasSuffix(path, ".yaml") {
			continue
		}
		if strings.HasPrefix(content, "# Generated by") {
			if nl := strings.IndexByte(content, '\n'); nl >= 0 {
				content = content[nl+1:]
			}
		}
		emit.Files[path] = banner + content
	}
	emit.Report = banner + emit.Report
}
