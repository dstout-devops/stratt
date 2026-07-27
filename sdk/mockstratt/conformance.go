package mockstratt

import (
	"fmt"
	"sort"
	"strings"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// The conformance suite: does this plugin honour the PORT, whatever it does with
// the tool underneath? This is ADR-0137 D2's teeth — the thing a CI gate can run
// against a plugin it knows nothing about.
//
// Every check here is tool-blind by construction. Nothing in this file knows what
// ansible, tofu or helm are, and nothing may learn: a conformance suite that grew
// an `if ansible {}` would be the §1.4 violation the whole plugin port exists to
// prevent, arriving through the test harness instead of the spine. The checks are
// derived from the port's own invariants and from failures this repo has actually
// shipped — each one below cites which.

// Severity separates "the port is broken" from "this looks wrong".
type Severity string

const (
	// SeverityError is a port violation: the real core would refuse, drop, or
	// mis-report this. A CI gate should fail.
	SeverityError Severity = "error"
	// SeverityWarn is a smell — legal on the wire, but the shape of a defect. It
	// exists because the alternative is not reporting it at all, and unreported
	// smells are how vacuous runs shipped.
	SeverityWarn Severity = "warn"
)

// Violation is one conformance failure. Deliberately NOT called a Finding: Finding
// is a Named Kind (§2) meaning a drift/compliance result bound to an Entity and a
// Baseline with Evidence behind it. This is a test verdict about a binary and
// borrowing the Kind's name would make the vocabulary mean two things.
type Violation struct {
	Check    string
	Severity Severity
	Detail   string
	// Why states the consequence in production, not the rule that was broken. A
	// conformance failure a plugin author cannot act on gets suppressed, and a
	// suppressed check is worse than no check.
	Why string
}

func (v Violation) String() string {
	return fmt.Sprintf("[%s] %s: %s — %s", v.Severity, v.Check, v.Detail, v.Why)
}

// Conformance is one governed exchange, ready to be judged.
type Conformance struct {
	Request Request
	Result  Result
	// Manifest is optional — the subprocess transport has none (a one-shot binary
	// answers no GetManifest). Manifest checks are skipped rather than failed when
	// it is nil: reporting a missing manifest as a violation for every EE-Job
	// plugin would train authors to ignore the output.
	Manifest *pluginv1.Manifest
}

// Check runs the suite and returns every violation, errors first.
func (c Conformance) Check() []Violation {
	var vs []Violation
	add := func(check string, sev Severity, detail, why string) {
		vs = append(vs, Violation{Check: check, Severity: sev, Detail: detail, Why: why})
	}

	// ── The stream terminated ────────────────────────────────────────────────
	// A stream that just stops is torn, not converged. The core folds this to
	// not-OK, so a plugin relying on "no news is good news" reports failure in
	// production for a reason its author will not find in their own logs.
	if !c.Result.SawTerminal {
		add("terminal-event", SeverityError, "no terminal TaskEvent was emitted",
			"the core folds a stream that never terminated to FAILED; the Run fails with no stated cause")
	}

	// ── A failure said why ───────────────────────────────────────────────────
	// ADR-0117 D5c is exactly this defect one layer down: the governor kept the
	// FACT of a red terminal and discarded its text, so failed Runs recorded no
	// cause at all. A plugin that emits a red terminal with no message recreates
	// that dead end from the other side, and the pod log is deleted with the Job.
	if c.Result.SawTerminal && c.Result.Failed() && strings.TrimSpace(c.Result.Error) == "" {
		add("failure-states-cause", SeverityError, "terminal event reported failure with an empty message",
			"the pod log is deleted with the Job, so descent shows FAILED and no reason anywhere (§1.8)")
	}

	// ── No confused-deputy emissions ─────────────────────────────────────────
	// The core holds the target set; a result keyed outside it is refused. A
	// plugin doing this is keying results off its own inventory rather than the
	// resolved set, which means its per-target status silently does not apply to
	// what was asked for.
	for _, r := range c.Result.Rejections {
		if r.Kind == "item-result" {
			add("resolved-targets-only", SeverityError, r.Detail,
				"the core refuses per-target status for an unresolved target; this target's outcome is reported nowhere")
		}
	}

	// ── Every resolved target was accounted for ──────────────────────────────
	// The vacuous-run guard, generalised. A Run that actuates nothing and reports
	// success is the most expensive failure this platform can produce, because it
	// is indistinguishable from convergence: the estate is untouched and every
	// screen is green.
	if len(c.Request.Targets) > 0 && c.Result.SawTerminal {
		var missing []string
		for _, t := range c.Request.Targets {
			if _, ok := c.Result.PerTarget[t.Name]; !ok {
				missing = append(missing, t.Name)
			}
		}
		sort.Strings(missing)
		switch {
		case len(missing) == len(c.Request.Targets) && c.Result.Succeeded:
			add("no-vacuous-success", SeverityError,
				fmt.Sprintf("%d resolved targets, 0 per-target results, terminal ok", len(missing)),
				"a green Run that actuated nothing is indistinguishable from convergence — the estate is untouched and every screen agrees")
		case len(missing) > 0:
			add("per-target-coverage", SeverityWarn, strings.Join(missing, ", "),
				"these targets have no reported outcome; descent can say the Run ran but not what happened to them (§1.8)")
		}
	}

	// ── Nothing was emitted beyond the grant ─────────────────────────────────
	// Each of these was DROPPED. In production the drop is silent to the plugin,
	// so a Syncer whose facets all fall outside its grant looks healthy while
	// writing nothing at all.
	// Shared with the Observe suite: what the governor drops does not depend on which
	// verb proposed it, so the two must not drift on what they report.
	vs = append(vs, checkGovernanceRejections(c.Result.Rejections)...)

	// ── Write-back correlates to something that was asked for ────────────────
	// A plugin proposing entities unrelated to its targets is either guessing or
	// projecting the whole world through an Apply. The core re-correlates
	// per-target write-back onto the resolved set (§1.2); what does not correlate
	// becomes an orphan nothing reconciles.
	if len(c.Request.Targets) > 0 {
		known := map[string]bool{}
		for _, t := range c.Request.Targets {
			ids := t.IdentityKeys
			if ids == nil {
				ids = map[string]string{"host.name": t.Name}
			}
			for scheme, v := range ids {
				known[scheme+"="+v] = true
			}
		}
		for _, e := range c.Result.WriteBack {
			var matched bool
			for scheme, v := range e.IdentityKeys {
				if known[scheme+"="+v] {
					matched = true
					break
				}
			}
			if !matched {
				add("write-back-correlates", SeverityWarn, e.Kind+" "+idString(e.IdentityKeys),
					"no identity key matches any resolved target; the core cannot correlate this to the Entity it actuated (§1.2)")
			}
		}
	}

	// ── Manifest ─────────────────────────────────────────────────────────────
	if m := c.Manifest; m != nil {
		if m.GetPluginId() == "" {
			add("manifest-identity", SeverityError, "plugin_id is empty",
				"the core binds ownership and provenance to this identity; empty means it can bind to nothing")
		}
		if m.GetProtocolVersion() == "" {
			add("manifest-protocol-version", SeverityError, "protocol_version is empty",
				"the envelope version is decoupled from contract hashes (invariant #5); without it the core cannot enforce its N-1 floor (§1.7)")
		}
		if m.GetClass() == pluginv1.PluginClass_PLUGIN_CLASS_UNSPECIFIED {
			add("manifest-class", SeverityError, "class is UNSPECIFIED",
				"the core routes by plugin class; unspecified is not a class and the plugin is unroutable")
		}
		if len(m.GetVerbs()) == 0 {
			add("manifest-verbs", SeverityError, "no verbs advertised",
				"the core calls only advertised verbs (invariant #2), so a plugin advertising none is never called")
		}
		for _, cd := range m.GetContracts() {
			if cd.GetSha256() == "" {
				add("contracts-hash-pinned", SeverityError, cd.GetSchemaId(),
					"§1.5 requires plugin schemas pinned and hash-verified; an unpinned contract makes schema drift silent")
			}
		}

		// ── It projects only what it advertised ──────────────────────────────
		//
		// `contracts` is the plugin's ADVERTISEMENT — the proto calls it "facet
		// namespaces it REQUESTS to own (advertisement, not grant)". It is what an
		// operator READS in order to write the grant. So a namespace the plugin
		// emits but never advertised has exactly two outcomes, and both are bad:
		// the grant omits it and the write is silently dropped (the sibling
		// `emits-within-grant` check catches that one, but only once someone runs
		// it against a realistic grant), or the grant happens to be wider and the
		// write LANDS under authority the operator granted without being told it
		// was being asked for (§2.5, §1.8).
		//
		// This is the gap that let ADR-0143's defect exist and then, immediately,
		// caught its fix: `mgmt.address` had been named as a vcenter-written
		// namespace in the Facet schema since ADR-0084 and was in neither the
		// grant nor the Manifest, and the first change to emit it updated the
		// grant and left the advertisement stale. Three sets — advertised,
		// granted, emitted — and until now only two of the three pairings were
		// checked anywhere.
		//
		// Deliberately compared against `contracts` only, never against a
		// hardcoded list: this file is tool-blind by construction and must stay
		// that way (§1.4).
		for _, v := range checkDeclaresWhatItEmits(m, c.Result.WriteBack) {
			vs = append(vs, v)
		}
		for _, a := range m.GetActions() {
			if a.GetName() == "" {
				add("action-named", SeverityError, "an ActionDecl has an empty name",
					"InvokeRequest.action selects by name; an unnamed Action cannot be invoked")
			}
			if in := a.GetInput(); in == nil || in.GetSha256() == "" {
				add("action-input-pinned", SeverityError, a.GetName(),
					"args are conformance-checked against a pinned input Contract; unpinned means unchecked (§1.5)")
			}
		}
	}

	sort.SliceStable(vs, func(i, j int) bool {
		return vs[i].Severity == SeverityError && vs[j].Severity != SeverityError
	})
	return vs
}

// Errors returns only the violations a CI gate should fail on.
func (c Conformance) Errors() []Violation { return errorsOnly(c.Check()) }

// errorsOnly is shared with ObserveConformance — both verbs answer the same
// question of a CI gate, so they must not drift on what counts as failing.
func errorsOnly(vs []Violation) []Violation {
	var out []Violation
	for _, v := range vs {
		if v.Severity == SeverityError {
			out = append(out, v)
		}
	}
	return out
}

// Report renders the full verdict for a test failure message or a CLI. It lists
// warnings as well as errors — a suite that prints only what it failed on teaches
// authors that warnings do not exist.
func (c Conformance) Report() string { return report("conformance", c.Check()) }

// report renders a verdict under a label, shared so the Apply and Observe suites
// read identically in a failure message.
func report(label string, vs []Violation) string {
	if len(vs) == 0 {
		return label + ": OK"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d violation(s)\n", label, len(vs))
	for _, v := range vs {
		fmt.Fprintf(&b, "  %s\n", v)
	}
	return b.String()
}

func idString(ids map[string]string) string {
	keys := make([]string, 0, len(ids))
	for k := range ids {
		keys = append(keys, k+"="+ids[k])
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
