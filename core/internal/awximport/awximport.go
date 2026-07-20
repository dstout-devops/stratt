// Package awximport transforms an enumerated AWX estate (a connectors/awx
// Snapshot) into a reviewable, Git-declared desired-state bundle: Views,
// Workflows, CredentialRefs, survey input Contracts, and a migration report
// (charter §5.6 "AWX exodus", Flow 6).
//
// The output is desired state, not the projection graph (§1.2): the importer
// never writes Entities and never fabricates a system of record. Where a
// faithful mapping is impossible (manual hosts, irreducible host filters,
// approver identity), it emits a best-effort declaration AND a blocking
// migration-report entry — the abstraction never hides the gap (§1.8).
//
// Emitted identifiers use Stratt vocabulary (View / Workflow / Step /
// CredentialRef / input Contract); AWX nouns appear only in the report's "was:"
// compat column and in provenance labels prefixed awx.* (§2).
package awximport

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dstout-devops/stratt/core/internal/awximport/awx"
)

// Options tunes the transform. Zero value is valid.
type Options struct {
	// AdoptedFrom, when set, stamps adopt lineage (ADR-0087) onto every emitted Workflow.
	// `stratt adopt` sets it for its single-object bundle; the legacy full-estate importer
	// leaves it nil. A pointer so the zero Options emits no lineage.
	AdoptedFrom *AdoptLineage
}

// AdoptLineage is the source-object lineage stamped onto an adopted Workflow.
type AdoptLineage struct {
	Kind     string
	Identity string
	Source   string
}

// Emit is the in-memory bundle: relative file path → content, plus the report.
// WriteBundle (write.go) is the only thing that touches the filesystem.
type Emit struct {
	Files  map[string]string
	Report string
}

// Bundle transforms a Snapshot into a desired-state bundle. It is pure (no I/O)
// so the mappings are unit-testable end to end.
func Bundle(snap *awx.Snapshot, opts Options) (*Emit, error) {
	e := &Emit{Files: map[string]string{}}
	r := newReport()

	// Views first: Workflows reference them by name.
	viewFor := map[int]string{} // inventory id → emitted View name
	for _, inv := range snap.Inventories {
		name, doc, err := mapInventory(snap, inv, r)
		if err != nil {
			return nil, err
		}
		viewFor[inv.ID] = name
		e.Files["views/"+slug(inv.Name)+".yaml"] = doc
	}

	// CredentialRefs.
	credName := map[int]string{}
	ids := make([]int, 0, len(snap.Credentials))
	for id := range snap.Credentials {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		cr := snap.Credentials[id]
		name, doc, err := mapCredential(cr, r)
		if err != nil {
			return nil, err
		}
		credName[id] = name
		e.Files["credential-refs/"+slug(cr.Name)+".yaml"] = doc
	}

	// Surveys → input Contracts.
	for _, jt := range snap.JobTemplates {
		spec, ok := snap.Surveys[jt.ID]
		if !ok {
			continue
		}
		doc, err := mapSurvey(jt, spec, r)
		if err != nil {
			return nil, err
		}
		e.Files["contracts/"+slug(jt.Name)+".survey.schema.json"] = doc
	}

	// Workflows share one namespace (both job templates and workflow job
	// templates become Stratt Workflows); dedupe slugs so filenames and
	// declaration names never collide.
	used := map[string]bool{}
	uniq := func(base string) string {
		s := base
		for i := 2; used[s]; i++ {
			s = fmt.Sprintf("%s-%d", base, i)
		}
		used[s] = true
		return s
	}

	// Job templates → single-Step Workflows.
	for _, jt := range snap.JobTemplates {
		s := uniq(slug(jt.Name))
		doc, err := mapJobTemplate(snap, jt, viewFor, credName, "awx/"+s, r, opts.AdoptedFrom)
		if err != nil {
			return nil, err
		}
		e.Files["workflows/"+s+".yaml"] = doc
	}

	// Workflow job templates → multi-Step Workflows.
	for _, wjt := range snap.WorkflowJTs {
		s := uniq(slug(wjt.Name))
		doc, err := mapWorkflow(snap, wjt, viewFor, credName, "awx/"+s, r, opts.AdoptedFrom)
		if err != nil {
			return nil, err
		}
		e.Files["workflows/"+s+".yaml"] = doc
	}

	e.Report = r.render(snap)
	return e, nil
}

// slug renders an AWX name as a filesystem- and declaration-safe identifier.
func slug(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unnamed"
	}
	return out
}

// docComment is a helper for consistent error context.
func mapErr(kind, name string, err error) error {
	return fmt.Errorf("awximport: %s %q: %w", kind, name, err)
}
