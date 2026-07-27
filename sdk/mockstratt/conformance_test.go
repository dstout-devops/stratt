package mockstratt

import (
	"context"
	"strings"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// The conformance suite, checked against plugins that behave and plugins that do
// not. A suite that only ever sees good input proves nothing about a gate.

func conform(t *testing.T, scenario string, req Request, h *Host) Conformance {
	t.Helper()
	if h == nil {
		h = NewHost(testGrant())
	}
	res := run(t, fake(t, scenario), h, req)
	return Conformance{Request: req, Result: res}
}

func hasCheck(vs []Violation, name string) bool {
	for _, v := range vs {
		if v.Check == name {
			return true
		}
	}
	return false
}

// TestConformance_WellBehavedPluginPasses is the baseline that keeps the suite
// honest: a plugin doing the right thing must produce NO violations. A suite that
// fires on correct behaviour gets muted, and a muted gate is not a gate.
func TestConformance_WellBehavedPluginPasses(t *testing.T) {
	c := conform(t, "ok", Request{Targets: targets("web-1", "web-2")}, nil)
	if vs := c.Check(); len(vs) != 0 {
		t.Fatalf("a conforming plugin must produce no violations:\n%s", c.Report())
	}
}

// TestConformance_VacuousSuccess is the check worth having above all the others: a
// Run that actuated nothing and reported success. It is the most expensive failure
// the platform can produce because it is indistinguishable from convergence — the
// estate is untouched and every screen is green.
func TestConformance_VacuousSuccess(t *testing.T) {
	c := conform(t, "vacuous", Request{Targets: targets("web-1", "web-2")}, nil)
	if !hasCheck(c.Errors(), "no-vacuous-success") {
		t.Fatalf("a green Run with zero per-target results must be an ERROR:\n%s", c.Report())
	}
}

// TestConformance_TornStream: the plugin died mid-stream. Reported separately from
// a failed host because the fixes are different — this one is a defect in the
// plugin, and a reader sent to the wrong place is a §1.8 failure of its own.
func TestConformance_TornStream(t *testing.T) {
	c := conform(t, "torn", Request{Targets: targets("web-1")}, nil)
	if !hasCheck(c.Errors(), "terminal-event") {
		t.Fatalf("a stream with no terminal must be an ERROR:\n%s", c.Report())
	}
}

// TestConformance_SilentFailure: failing without saying why. The pod log is
// deleted with the Job, so descent shows FAILED and no reason anywhere.
func TestConformance_SilentFailure(t *testing.T) {
	c := conform(t, "silent-failure", Request{Targets: targets("web-1")}, nil)
	if !hasCheck(c.Errors(), "failure-states-cause") {
		t.Fatalf("a red terminal with no message must be an ERROR:\n%s", c.Report())
	}
}

// TestConformance_LoudFailureIsNotAViolation is the necessary counterweight: a
// plugin that fails HONESTLY — a stated reason and a per-target status — conforms.
// Failure is not non-conformance, and a suite that conflated the two would push
// authors toward hiding failures, which is the §1.8 outcome we least want.
func TestConformance_LoudFailureIsNotAViolation(t *testing.T) {
	c := conform(t, "loud-failure", Request{Targets: targets("web-1")}, nil)
	if vs := c.Errors(); len(vs) != 0 {
		t.Fatalf("an honestly-reported failure must not be a conformance error:\n%s", c.Report())
	}
}

// TestConformance_ConfusedDeputy: reporting on a target nobody asked for.
func TestConformance_ConfusedDeputy(t *testing.T) {
	c := conform(t, "confused-deputy", Request{Targets: targets("web-1")}, nil)
	errs := c.Errors()
	if !hasCheck(errs, "resolved-targets-only") {
		t.Fatalf("a result keyed outside the resolved set must be an ERROR:\n%s", c.Report())
	}
	// The same run is also vacuous with respect to what WAS asked for, and both
	// belong in the report: fixing the key without actuating web-1 is still broken.
	if !hasCheck(errs, "no-vacuous-success") {
		t.Errorf("web-1 got no outcome either; that must be reported too:\n%s", c.Report())
	}
}

// TestConformance_Overreach: everything emitted beyond the grant. In production
// each of these is dropped SILENTLY, so the plugin looks healthy while writing
// nothing — which is why they are errors here rather than warnings.
func TestConformance_Overreach(t *testing.T) {
	h := NewHost(testGrant()).WithFacetWriteScope("os.kernel", "app.config")
	c := conform(t, "overreach", Request{Targets: targets("web-1")}, h)
	errs := c.Errors()
	if !hasCheck(errs, "emits-within-grant") {
		t.Fatalf("emissions outside the grant must be errors:\n%s", c.Report())
	}
	report := c.Report()
	for _, want := range []string{"billing", "secret", "dns.fqdn"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report must name the specific refusal %q so it can be acted on:\n%s", want, report)
		}
	}
}

// TestConformance_WriteBackMustCorrelate: an entity whose identity matches no
// resolved target cannot be correlated onto what was actuated (§1.2).
func TestConformance_WriteBackMustCorrelate(t *testing.T) {
	g := testGrant()
	g.Tier = TierTrusted // so dns.fqdn survives the tier gate and reaches THIS check
	h := NewHost(g).WithFacetWriteScope("os.kernel", "app.config")
	c := conform(t, "overreach", Request{Targets: targets("db-9")}, h)
	if !hasCheck(c.Check(), "write-back-correlates") {
		t.Fatalf("write-back that matches no resolved target must be reported:\n%s", c.Report())
	}
}

// TestConformance_ManifestChecksAreSkippedWhenAbsent: a one-shot EE-Job binary
// answers no GetManifest. Failing every such plugin for a missing manifest would
// train authors to ignore the output, so absence is skipped, not failed.
func TestConformance_ManifestChecksAreSkippedWhenAbsent(t *testing.T) {
	c := conform(t, "ok", Request{Targets: targets("web-1")}, nil)
	if c.Manifest != nil {
		t.Fatal("the subprocess transport has no manifest")
	}
	for _, v := range c.Check() {
		if strings.HasPrefix(v.Check, "manifest-") {
			t.Fatalf("manifest checks must be skipped when there is no manifest, got %s", v)
		}
	}
}

// TestConformance_ManifestChecks covers the gRPC half: identity, version, class,
// verbs, and — the §1.5 one — hash-pinned contracts.
func TestConformance_ManifestChecks(t *testing.T) {
	c := Conformance{
		Result: Result{SawTerminal: true, Succeeded: true},
		Manifest: &pluginv1.Manifest{
			// plugin_id, protocol_version, class and verbs all absent.
			Contracts: []*pluginv1.ContractDecl{{SchemaId: "fake-source/thing"}}, // unpinned
			Actions:   []*pluginv1.ActionDecl{{Name: "make-thing"}},              // unpinned input
		},
	}
	errs := c.Errors()
	for _, want := range []string{
		"manifest-identity", "manifest-protocol-version", "manifest-class",
		"manifest-verbs", "contracts-hash-pinned", "action-input-pinned",
	} {
		if !hasCheck(errs, want) {
			t.Errorf("missing check %q:\n%s", want, c.Report())
		}
	}
}

// TestConformance_ReportListsWarningsToo: a suite that prints only what it failed
// on teaches authors that warnings do not exist.
func TestConformance_ReportListsWarningsToo(t *testing.T) {
	req := Request{Targets: targets("web-1", "web-2", "web-3")}
	res := run(t, fake(t, "ok"), NewHost(testGrant()), Request{Targets: targets("web-1")})
	c := Conformance{Request: req, Result: res} // two targets got no outcome
	if len(c.Errors()) != 0 {
		t.Fatalf("partial coverage is a warning, not an error:\n%s", c.Report())
	}
	if !strings.Contains(c.Report(), "web-2") {
		t.Fatalf("the report must surface warnings:\n%s", c.Report())
	}
}

// TestConformance_ErrorsAreOrderedFirst: the report is read top-down under time
// pressure, so what must be fixed comes before what might.
func TestConformance_ErrorsAreOrderedFirst(t *testing.T) {
	res, err := fake(t, "vacuous").Run(context.Background(), NewHost(testGrant()), Request{Targets: targets("web-1")})
	if err != nil {
		t.Fatal(err)
	}
	vs := Conformance{Request: Request{Targets: targets("web-1")}, Result: res}.Check()
	var seenWarn bool
	for _, v := range vs {
		if v.Severity == SeverityWarn {
			seenWarn = true
		} else if seenWarn {
			t.Fatalf("errors must sort before warnings: %+v", vs)
		}
	}
}
