package desiredstate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/provision"
	"github.com/dstout-devops/stratt/types"
)

// estateRoot is the repo's reconciled reference estate (/estate), relative to
// this package dir (core/internal/desiredstate → repo root).
const estateRoot = "../../../estate"

// The estate's production-slice Triggers (ADR-0057): each fires a Run needing a
// plugin or target set the dev cell doesn't run (openbao, real host fleets,
// the Salt event bus). Tagged `environments: [prod]`, so a dev daemon skips them.
var prodTriggers = map[string]bool{
	"cert-reconcile-web": true,
	"fileset-collector":  true,
	"access-collector":   true,
	"salt-minion-start":  true,
}

// TestReferenceEstateParses is the standing guard that the shipped /estate stays
// a valid, reconcilable desired-state tree: every declaration parses and passes
// its load-time validation (the same ParseDir the daemon runs at boot). A broken
// example added to /estate fails CI here, not silently in a dev cluster.
func TestReferenceEstateParses(t *testing.T) {
	if _, err := os.Stat(estateRoot); err != nil {
		t.Skipf("reference estate not found at %s (%v)", estateRoot, err)
	}
	decls, err := ParseDir(estateRoot, nil)
	if err != nil {
		t.Fatalf("reference estate %s does not parse/validate: %v", filepath.Clean(estateRoot), err)
	}
	// Sanity: the flagship pieces are present (loose lower bounds — the estate
	// grows, so assert presence, not an exact census).
	if len(decls.Views) < 5 {
		t.Errorf("expected the reference estate to declare several Views, got %d", len(decls.Views))
	}
	if !hasView(decls, "linux-fleet") {
		t.Error("reference estate is missing the flagship linux-fleet View")
	}
	// Every Trigger we consider a prod trigger must actually exist and be tagged
	// prod; every other Trigger must be untagged (in every environment). This
	// keeps the tagging honest as triggers are added.
	seenProd := map[string]bool{}
	for _, tr := range decls.Triggers {
		tagged := len(tr.Environments) > 0
		if prodTriggers[tr.Name] {
			seenProd[tr.Name] = true
			if !tagged || !contains(tr.Environments, "prod") {
				t.Errorf("trigger %q must be tagged environments:[prod], got %v", tr.Name, tr.Environments)
			}
		} else if tagged {
			t.Errorf("trigger %q is unexpectedly env-tagged (%v); add it to prodTriggers or untag it", tr.Name, tr.Environments)
		}
	}
	for name := range prodTriggers {
		if !seenProd[name] {
			t.Errorf("expected prod trigger %q in the reference estate, not found", name)
		}
	}
}

// TestReferenceEstateDevSlice proves ADR-0057 on the REAL estate: a dev daemon's
// apply set is the whole estate minus the prod-tagged Triggers — no cross-env
// schedule noise — while Views (incl. the plugin-e2e fixtures) are never filtered.
func TestReferenceEstateDevSlice(t *testing.T) {
	if _, err := os.Stat(estateRoot); err != nil {
		t.Skipf("reference estate not found at %s (%v)", estateRoot, err)
	}
	decls, err := ParseDir(estateRoot, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	dev := ScopeToEnvironment(decls, "dev")
	for _, tr := range dev.Triggers {
		if prodTriggers[tr.Name] {
			t.Errorf("dev slice must NOT contain prod trigger %q", tr.Name)
		}
	}
	// The plugin-e2e fixture Views survive scoping (Views are never env-filtered).
	if !hasView(dev, "dev-hosts") || !hasView(dev, "dev-vms") {
		t.Error("dev slice dropped a plugin-e2e fixture View (dev-hosts/dev-vms) — Views must never be env-filtered")
	}
	// The untagged launching kinds (Assignments) all survive.
	if len(dev.Assignments) != len(decls.Assignments) {
		t.Errorf("dev slice changed Assignment count %d→%d; the estate's Assignments are untagged (all envs)",
			len(decls.Assignments), len(dev.Assignments))
	}

	// The prod slice is the mirror image: the four schedule/event triggers ARE
	// present where their plugins live.
	prod := ScopeToEnvironment(decls, "prod")
	if len(prod.Triggers) != len(decls.Triggers) {
		t.Errorf("prod slice must keep every Trigger, got %d of %d", len(prod.Triggers), len(decls.Triggers))
	}

	// Unscoped (env == "") is byte-identical to no scoping: every Trigger kept.
	if all := ScopeToEnvironment(decls, ""); len(all.Triggers) != len(decls.Triggers) {
		t.Errorf("unscoped daemon must keep every Trigger, got %d of %d", len(all.Triggers), len(decls.Triggers))
	}
}

// TestReferenceEstateProvisioningIntent ties the real estate's Intent/Compute
// (web-fleet) through the provisioning planner (ADR-0058): with nothing built,
// the whole declared fleet surfaces as gated builds — web-01, web-02 — and the
// planner produces no phantom, no error. This is the declare→gated-build seam
// exercised on the shipped declaration, not a synthetic one.
func TestReferenceEstateProvisioningIntent(t *testing.T) {
	if _, err := os.Stat(estateRoot); err != nil {
		t.Skipf("reference estate not found at %s (%v)", estateRoot, err)
	}
	decls, err := ParseDir(estateRoot, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var pis []provision.Intent
	for _, in := range decls.Intents {
		if in.Kind != types.IntentCompute {
			continue
		}
		pi, err := provision.FromIntent(in)
		if err != nil {
			t.Fatalf("decode Intent/Compute %q: %v", in.Name, err)
		}
		pis = append(pis, pi)
	}
	if len(pis) == 0 {
		t.Fatal("expected an Intent/Compute (web-fleet) in the reference estate")
	}
	// Nothing built → the whole fleet is a gated-build shortfall (no phantom).
	res, err := provision.Plan(pis, nil, 0)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got := map[string]bool{}
	for _, i := range res.ToBuild {
		got[i.Name] = true
	}
	if !got["web-01"] || !got["web-02"] {
		t.Errorf("web-fleet (count:2) must surface web-01+web-02 for build, got %v", got)
	}
	if len(res.Paused) != 0 {
		t.Errorf("a 2-instance fleet must not trip the max-delta gate, got %v", res.Paused)
	}
}

func hasView(d Declarations, name string) bool {
	for _, v := range d.Views {
		if v.Name == name {
			return true
		}
	}
	return false
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
