package mockstratt

import (
	"sort"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Conformance for the OBSERVE verb — the Syncer half of the port.
//
// WHY THIS EXISTS AS A SEPARATE TYPE. The suite in conformance.go is
// Apply/Invoke-shaped: it judges a terminal TaskEvent, a stated failure cause, and
// per-target accounting. An Observe window has none of those — it is a set of
// projection proposals plus refusals, and `Conn.Observe` returns an ObserveResult
// rather than a Result for exactly that reason.
//
// The consequence was that **no Syncer could be conformance-checked at all**, and
// it shows: of the plugins in this repo, one has a conformance test, and it is an
// Actuator. ADR-0137 D2 calls the suite "the thing a CI gate can run against a
// plugin it knows nothing about" — that only held for half the port.
//
// Faking the missing fields was the alternative and it is worse than no check: a
// Result with SawTerminal:true invented so the Apply suite would pass makes
// `terminal-event` report a lie, and a check that can only pass teaches authors
// the output is noise.
//
// The checks below are the ones that are genuinely verb-independent — what a
// projection is allowed to contain, and whether the plugin projected what it
// advertised. They are shared with the Apply suite rather than copied.
type ObserveConformance struct {
	Result ObserveResult
	// Manifest is optional, on the same convention as Conformance: skipped rather
	// than failed when nil.
	Manifest *pluginv1.Manifest
}

// Check runs the Observe suite and returns every violation, errors first.
func (c ObserveConformance) Check() []Violation {
	var vs []Violation

	// Refusals the host already made. In production these become Findings the
	// plugin author never sees, so surfacing them at development time is most of
	// the value this package offers (§1.8).
	vs = append(vs, checkGovernanceRejections(c.Result.Rejections)...)

	// A Syncer that under-reports a FULL sync deletes estate — the most dangerous
	// thing any plugin does. This does not judge whether the window was complete
	// (only the Source knows), but a window that claims neither completeness nor a
	// cursor leaves the core unable to tell "done" from "more to come", and the
	// tombstone decision hangs on exactly that (ADR-0042).
	if !c.Result.FullSyncComplete && c.Result.NextCursor == "" && len(c.Result.Entities) > 0 {
		vs = append(vs, Violation{
			Check: "observe-window-terminates", Severity: SeverityWarn,
			Detail: "window reported entities but neither full_sync_complete nor a next cursor",
			Why: "the core cannot distinguish a finished full sync from a truncated one, and tombstoning " +
				"turns on that distinction — a window that under-reports DELETES estate (ADR-0042)",
		})
	}

	if c.Manifest != nil {
		vs = append(vs, checkDeclaresWhatItEmits(c.Manifest, c.Result.Entities)...)
	}

	sort.SliceStable(vs, func(i, j int) bool {
		return vs[i].Severity == SeverityError && vs[j].Severity != SeverityError
	})
	return vs
}

// Errors returns only the violations a CI gate should fail on.
func (c ObserveConformance) Errors() []Violation { return errorsOnly(c.Check()) }

// Report renders the full verdict — warnings included, for the reason Conformance.Report
// states: a suite that prints only what it failed on teaches authors that warnings do not exist.
func (c ObserveConformance) Report() string { return report("OBSERVE conformance", c.Check()) }

// ── shared checks ───────────────────────────────────────────────────────────

// checkDeclaresWhatItEmits is the advertised/emitted pairing, shared by both verbs.
//
// `contracts` is the plugin's ADVERTISEMENT — the proto calls it "facet namespaces it
// REQUESTS to own (advertisement, not grant)" — and it is what an operator READS in order
// to write the grant. A namespace emitted but never advertised has two outcomes and both
// are bad: the grant omits it and the write is silently dropped, or the grant is wider and
// the write LANDS under authority the operator granted without being told it was wanted
// (§1.8/§2.5).
//
// A Manifest advertising no contracts at all is an Actuator-shaped plugin owning no
// namespace; it is not judged against an empty advertisement.
//
// Compared against `contracts` only, never a hardcoded list — this package is tool-blind by
// construction and must stay that way (§1.4).
func checkDeclaresWhatItEmits(m *pluginv1.Manifest, entities []Entity) []Violation {
	if m == nil || len(m.GetContracts()) == 0 {
		return nil
	}
	advertised := make(map[string]bool, len(m.GetContracts()))
	for _, cd := range m.GetContracts() {
		advertised[cd.GetSchemaId()] = true
	}
	var vs []Violation
	seen := map[string]bool{}
	for _, e := range entities {
		// Sorted, so the report is stable across runs rather than map-order noise.
		nss := make([]string, 0, len(e.Facets))
		for ns := range e.Facets {
			nss = append(nss, ns)
		}
		sort.Strings(nss)
		for _, ns := range nss {
			if advertised[ns] || seen[ns] {
				continue
			}
			seen[ns] = true
			vs = append(vs, Violation{
				Check: "declares-what-it-emits", Severity: SeverityError, Detail: ns,
				Why: "this Facet namespace is projected but absent from the Manifest's contracts — the operator " +
					"writes the grant from that advertisement, so this either gets dropped or lands under " +
					"authority nobody was asked for (§1.8/§2.5)",
			})
		}
	}
	return vs
}

// checkGovernanceRejections turns host refusals into violations. Shared, because what the
// governor drops does not depend on which verb proposed it.
func checkGovernanceRejections(rejections []Rejection) []Violation {
	var vs []Violation
	for _, r := range rejections {
		switch r.Kind {
		case "facet", "label", "identity-scheme", "entity":
			vs = append(vs, Violation{
				Check: "emits-within-grant", Severity: SeverityError, Detail: r.Kind + " " + r.Detail,
				Why: "the core dropped this silently — the plugin appears healthy while writing nothing (" + r.Reason + ")",
			})
		case "derived-contract":
			vs = append(vs, Violation{
				Check: "derived-contract-namespace", Severity: SeverityError, Detail: r.Detail,
				Why: "a schema_id outside the plugin's own Source namespace is refused (ADR-0047 §4)",
			})
		case "tombstone-scheme":
			vs = append(vs, Violation{
				Check: "tombstones-within-grant", Severity: SeverityError, Detail: r.Detail,
				Why: "a tombstone on an ungranted scheme is refused; liveness for those Entities is silently never asserted (ADR-0042)",
			})
		}
	}
	return vs
}
