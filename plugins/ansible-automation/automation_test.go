// Package automation_test holds the ONE-PLUGIN-TWO-SOURCES invariants (ADR-0127 D1) —
// the properties that live BETWEEN the two halves and that neither half's own test can
// see. Every assertion here is a claim the ADR makes in prose; if a later change merges
// the manifests, widens a grant, or lets one half advertise the other's namespaces, this
// file is what says so.
//
// It is deliberately at the module root rather than inside a half: the subject is the
// relationship, not either package.
package automation_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/dstout-devops/stratt/plugins/ansible-automation/content"
	"github.com/dstout-devops/stratt/plugins/ansible-automation/controller"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// manifests fetches both halves' Manifests exactly as pluginhost.Register would.
func manifests(t *testing.T) (ctrl, cont *pluginv1.Manifest) {
	t.Helper()
	cs := controller.NewServer(controller.ServerConfig{}, controller.New(controller.Config{Endpoint: "https://aap.example.com"}), nil, quiet())
	ct := content.NewServer(content.ServerConfig{}, content.New(content.Config{Root: "."}), quiet())

	cr, err := cs.GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatalf("controller GetManifest: %v", err)
	}
	nr, err := ct.GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatalf("content GetManifest: %v", err)
	}
	return cr.GetManifest(), nr.GetManifest()
}

func contractIDs(m *pluginv1.Manifest) map[string]bool {
	out := map[string]bool{}
	for _, c := range m.GetContracts() {
		out[c.GetSchemaId()] = true
	}
	return out
}

// ONE plugin: both halves assert the SAME identity, because the operator installs one
// thing. The grant is keyed on this string (anti-spoof), so a divergence here would mean
// two grants under two names — the exact operator-facing problem ADR-0127 removed.
func TestBothHalvesAssertOnePluginIdentity(t *testing.T) {
	ctrl, cont := manifests(t)
	const want = "ansible-automation"
	if got := ctrl.GetPluginId(); got != want {
		t.Errorf("controller plugin_id = %q, want %q", got, want)
	}
	if got := cont.GetPluginId(); got != want {
		t.Errorf("content plugin_id = %q, want %q", got, want)
	}
}

// TWO Sources: the halves' owned namespaces must be DISJOINT. This is the assertion that
// makes the split safe — every Observe is a full sync driving a per-Source tombstone
// sweep, so an overlap would let one half's sync retract the other half's entities
// (ADR-0042 per-source liveness). Their union is the nine `ansible.*` namespaces.
func TestHalvesOwnDisjointNamespaces(t *testing.T) {
	ctrl, cont := manifests(t)
	c, n := contractIDs(ctrl), contractIDs(cont)

	for ns := range c {
		if n[ns] {
			t.Errorf("namespace %q is advertised by BOTH halves — a shared owner means one half's full sync retracts the other's entities (ADR-0042/0127 D1)", ns)
		}
	}
	if len(c) != 9 {
		t.Errorf("controller advertises %d contracts, want 9 (template/workflow/schedule/org/team/credential/user/label/executionenvironment)", len(c))
	}
	if len(n) != 4 {
		t.Errorf("content advertises %d contracts, want 4 (playbook/role/collection/inventory)", len(n))
	}
	for _, ns := range []string{"ansible.template", "ansible.workflow", "ansible.schedule", "ansible.org", "ansible.team", "ansible.credential", "ansible.user", "ansible.label", "ansible.executionenvironment"} {
		if !c[ns] {
			t.Errorf("controller half does not advertise %q", ns)
		}
	}
	for _, ns := range []string{"ansible.playbook", "ansible.role", "ansible.collection", "ansible.inventory"} {
		if !n[ns] {
			t.Errorf("content half does not advertise %q", ns)
		}
	}
}

// The CROSS-SOURCE `runs` edge ADR-0085's orphan Finding rests on: the controller half
// points AT `ansible.playbook` but must never OWN it. Advertising it as a contract would
// make the edge same-source, the target auto-resolvable, and the orphan signal
// meaningless (ADR-0042/0082 — an edge to an unprojected playbook is DROPPED, never
// vivified, and that dropped edge IS the signal).
func TestControllerPointsAtPlaybookButNeverOwnsIt(t *testing.T) {
	ctrl, _ := manifests(t)
	if contractIDs(ctrl)["ansible.playbook"] {
		t.Fatal("controller half advertises ansible.playbook as a facet contract — it must be POINTABLE-ONLY (granted as an IdentityScheme, never a FacetNamespace), or the cross-source runs edge collapses (ADR-0085/0127 D1)")
	}
	for _, ts := range ctrl.GetTombstoneSchemes() {
		if ts == "ansible.playbook" {
			t.Fatal("controller half tombstones ansible.playbook — it would retract entities owned by the content half's Source")
		}
	}
}

// §2.1 / ADR-0079 slice-3 gate, asserted at the manifest: the controller half must never
// advertise `identity.subject`. That namespace has a single write-owner (the SCIM identity
// projector) and a second claimant is a registration error, not a merge — which is exactly
// why ADR-0130 mirrors AWX accounts as `ansible.user` instead.
func TestControllerNeverClaimsTheIdentityPlane(t *testing.T) {
	ctrl, cont := manifests(t)
	for name, m := range map[string]*pluginv1.Manifest{"controller": ctrl, "content": cont} {
		if contractIDs(m)["identity.subject"] {
			t.Errorf("%s half advertises identity.subject — solely the SCIM identity projector's (§2.1/ADR-0079); an AWX local account is not an identity", name)
		}
	}
}

// A manifest advertising the UNION could register as neither half. This is the ADR-0127
// D1 correction stated as a test: pluginhost.Register rejects every contract outside the
// dialing grant's FacetNamespaces, so one address serving both halves registers as
// nothing. The invariant that keeps that from happening is that neither manifest is a
// superset of the other — asserted here so the property is checked, not just documented.
func TestNeitherManifestIsTheUnion(t *testing.T) {
	ctrl, cont := manifests(t)
	c, n := contractIDs(ctrl), contractIDs(cont)
	all := len(c) + len(n)
	if len(c) == all || len(n) == all {
		t.Fatal("one half advertises every ansible.* namespace — that manifest cannot register under either half's grant (ADR-0127 D1, corrected)")
	}
}

// §2.5, and the reason the role selector is a privilege boundary and not just a switch:
// the adopt/materialize Action lives on the CONTROLLER half only. A content-only install
// must therefore advertise no Action at all — it brokers no Secret and has nothing to
// invoke.
func TestOnlyTheControllerHalfAdvertisesAnAction(t *testing.T) {
	ctrl, cont := manifests(t)
	if len(cont.GetActions()) != 0 {
		t.Errorf("content half advertises %d Actions, want 0 — it holds no credential and invokes nothing (§2.5)", len(cont.GetActions()))
	}
	var found bool
	for _, a := range ctrl.GetActions() {
		if a.GetName() == "adopt/materialize" {
			found = true
		}
	}
	if !found {
		t.Error("controller half does not advertise adopt/materialize (ADR-0089)")
	}
}

// Both halves are POLL-mode Syncers with the OBSERVE verb — the property strattd's
// home-supervised SyncLoop assumes for each. Only the controller adds INVOKE.
func TestBothHalvesObserveByPolling(t *testing.T) {
	ctrl, cont := manifests(t)
	for name, m := range map[string]*pluginv1.Manifest{"controller": ctrl, "content": cont} {
		var observes bool
		for _, v := range m.GetVerbs() {
			if v == pluginv1.Verb_VERB_OBSERVE {
				observes = true
			}
		}
		if !observes {
			t.Errorf("%s half does not advertise OBSERVE — a Syncer grant requires it", name)
		}
		if m.GetObserveMode() != pluginv1.Manifest_OBSERVE_MODE_POLL {
			t.Errorf("%s half observe_mode = %v, want POLL", name, m.GetObserveMode())
		}
	}
}
