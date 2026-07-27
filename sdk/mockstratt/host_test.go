package mockstratt

import (
	"context"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// The governor's rules. Each of these is a refusal the REAL core makes, and the
// reason this package is worth having at all: a mock that admitted any of them
// would certify plugins that production drops on the floor.

func testGrant() Grant {
	return Grant{
		PluginIdentity:  "fake",
		Tier:            TierCommunity,
		SourceName:      "fake-source",
		FacetNamespaces: []string{"os.kernel", "app.config"},
		LabelKeys:       []string{"env"},
		IdentitySchemes: []string{"host.name", "dns.fqdn"},
	}
}

func govern(t *testing.T, h *Host, targets []ApplyTarget, frames ...*pluginv1.ApplyResponse) Result {
	t.Helper()
	res, err := h.Govern(context.Background(), &chanStream{frames: frames}, targets)
	if err != nil {
		t.Fatalf("govern: %v", err)
	}
	return res
}

// TestTerminalOkIsNotBelieved is the asymmetry, and it is the single most
// important rule in the governor: a plugin may declare its own FAILURE (nothing is
// better placed to know), but it may not declare a SUCCESS its per-target results
// contradict. Without this, a plugin that swallows a host failure and ends green
// turns a broken Run into a converged one.
func TestTerminalOkIsNotBelieved(t *testing.T) {
	res := govern(t, NewHost(testGrant()), []ApplyTarget{{Name: "web-1"}},
		result("web-1", pluginv1.ItemResult_STATUS_FAILED),
		terminal(true, "converged"),
	)
	if res.Succeeded {
		t.Fatal("a green terminal contradicted by a FAILED target must fold to not-OK — otherwise a plugin can declare success it did not achieve")
	}
}

// TestTerminalFailureIsBelievedWithItsMessage: the other half. A red terminal is
// trusted, AND its text is kept — the governor once discarded that text, leaving
// failed Runs with no cause anywhere once the pod log was gone (ADR-0117 D5c).
func TestTerminalFailureIsBelievedWithItsMessage(t *testing.T) {
	res := govern(t, NewHost(testGrant()), nil, terminal(false, "git clone refused"))
	if res.Succeeded {
		t.Fatal("a plugin declaring its own failure must be believed")
	}
	if res.Error != "git clone refused" {
		t.Fatalf("the plugin's own account of the failure must survive, got %q", res.Error)
	}
}

// TestNoTerminalIsFailure: a stream that just stops is torn, not converged.
func TestNoTerminalIsFailure(t *testing.T) {
	res := govern(t, NewHost(testGrant()), []ApplyTarget{{Name: "web-1"}},
		result("web-1", pluginv1.ItemResult_STATUS_OK),
	)
	if res.Succeeded {
		t.Fatal("a stream that never terminated must fold to not-OK (the §1.8 silent-death floor)")
	}
	if res.SawTerminal {
		t.Error("SawTerminal must stay false so the reader can tell a torn stream from a failed host")
	}
}

// TestConfusedDeputyTargetGate: the core holds the resolved set; a plugin's
// self-reported inventory never widens it.
func TestConfusedDeputyTargetGate(t *testing.T) {
	res := govern(t, NewHost(testGrant()), []ApplyTarget{{Name: "web-1"}},
		result("web-1", pluginv1.ItemResult_STATUS_OK),
		result("db-1", pluginv1.ItemResult_STATUS_OK), // never resolved
		terminal(true, "done"),
	)
	if _, ok := res.PerTarget["db-1"]; ok {
		t.Fatal("a per-target status for an unresolved target must be refused, never recorded")
	}
	if len(res.Rejections) != 1 || res.Rejections[0].Kind != "item-result" {
		t.Fatalf("the refusal must be surfaced, not silent: %+v", res.Rejections)
	}
}

// TestStickyFailure: a target that failed is never downgraded by a later OK. A
// plugin that retries and reports both outcomes must not end up looking clean.
func TestStickyFailure(t *testing.T) {
	res := govern(t, NewHost(testGrant()), []ApplyTarget{{Name: "web-1"}},
		result("web-1", pluginv1.ItemResult_STATUS_FAILED),
		result("web-1", pluginv1.ItemResult_STATUS_OK),
		terminal(true, "done"),
	)
	if res.PerTarget["web-1"] != "failed" {
		t.Fatalf("a failed target must never be downgraded, got %q", res.PerTarget["web-1"])
	}
}

// TestFacetCeilingIsGrantAndScope pins the pure AND (ADR-0054). Both bounds are
// exercised in one run, because the failure mode worth catching is a fallback
// creeping in — either bound quietly becoming "use the other one".
func TestFacetCeilingIsGrantAndScope(t *testing.T) {
	h := NewHost(testGrant()).WithFacetWriteScope("os.kernel") // granted: os.kernel, app.config
	res := govern(t, h, []ApplyTarget{{Name: "web-1"}},
		&pluginv1.ApplyResponse{WriteBack: []*pluginv1.ObservedEntity{{
			Kind:         "host",
			IdentityKeys: map[string]string{"host.name": "web-1"},
			Facets: map[string][]byte{
				"os.kernel":  []byte(`{"v":1}`), // granted AND scoped -> written
				"app.config": []byte(`{"v":2}`), // granted, NOT scoped -> dropped
				"billing":    []byte(`{"v":3}`), // neither -> dropped
			},
		}}},
		terminal(true, "done"),
	)
	if len(res.WriteBack) != 1 {
		t.Fatalf("expected one governed entity, got %d", len(res.WriteBack))
	}
	got := res.WriteBack[0].Facets
	if _, ok := got["os.kernel"]; !ok {
		t.Error("a facet inside BOTH bounds must be written")
	}
	if _, ok := got["app.config"]; ok {
		t.Error("granted but out of the Step's write-scope must drop — the scope is a floor, not a hint")
	}
	if _, ok := got["billing"]; ok {
		t.Error("ungranted must drop")
	}
	if len(res.Rejections) != 2 {
		t.Errorf("both drops must be surfaced, got %+v", res.Rejections)
	}
}

// TestNilWriteScopeAdmitsNothing: least authority. This is the tight default, and
// it is also the single most common reason a plugin author's facets "vanish" —
// which is exactly why it must produce a Rejection that says so.
func TestNilWriteScopeAdmitsNothing(t *testing.T) {
	res := govern(t, NewHost(testGrant()), []ApplyTarget{{Name: "web-1"}},
		&pluginv1.ApplyResponse{WriteBack: []*pluginv1.ObservedEntity{{
			Kind:         "host",
			IdentityKeys: map[string]string{"host.name": "web-1"},
			Facets:       map[string][]byte{"os.kernel": []byte(`{}`)},
		}}},
		terminal(true, "done"),
	)
	if len(res.WriteBack[0].Facets) != 0 {
		t.Fatal("no write-scope must admit NO facet write-back (least authority)")
	}
	if len(res.Rejections) == 0 {
		t.Fatal("and it must SAY so — a silently empty write-back is the defect this harness exists to surface")
	}
}

// TestSharedIdentitySchemeNeedsTrustedTier: tier AND grant. dns.fqdn is in the
// grant here; the community tier still refuses it, because a shared scheme can
// merge Entities across Sources and that is estate-wide power.
func TestSharedIdentitySchemeNeedsTrustedTier(t *testing.T) {
	frames := []*pluginv1.ApplyResponse{
		{WriteBack: []*pluginv1.ObservedEntity{{
			Kind:         "host",
			IdentityKeys: map[string]string{"dns.fqdn": "web-1.example.com"},
		}}},
		terminal(true, "done"),
	}

	community := govern(t, NewHost(testGrant()), nil, frames...)
	if len(community.WriteBack) != 0 {
		t.Fatal("a community-tier plugin must not emit a shared cross-source identity scheme, even when granted")
	}

	g := testGrant()
	g.Tier = TierTrusted
	trusted := govern(t, NewHost(g), nil, frames...)
	if len(trusted.WriteBack) != 1 {
		t.Fatal("the trusted tier must be allowed the same emission — the gate is tier AND grant, not tier alone")
	}
}

// TestEntityWithNoGrantedIdentityIsDroppedWhole: not written with what survived.
// A partially-identified entity cannot be correlated, so writing it would create
// an orphan nothing can ever reconcile or tombstone.
func TestEntityWithNoGrantedIdentityIsDroppedWhole(t *testing.T) {
	res := govern(t, NewHost(testGrant()).WithFacetWriteScope("os.kernel"), nil,
		&pluginv1.ApplyResponse{WriteBack: []*pluginv1.ObservedEntity{{
			Kind:         "host",
			IdentityKeys: map[string]string{"vcenter.uuid": "42"}, // ungranted
			Facets:       map[string][]byte{"os.kernel": []byte(`{}`)},
		}}},
		terminal(true, "done"),
	)
	if len(res.WriteBack) != 0 {
		t.Fatal("an entity with no granted identity key must be dropped whole, never written with its facets alone")
	}
}

// TestDerivedContractNamespaceConfinement: a plugin may declare schemas about its
// own outputs, inside its own Source namespace, and nowhere else (ADR-0047 §4).
func TestDerivedContractNamespaceConfinement(t *testing.T) {
	res := govern(t, NewHost(testGrant()), nil,
		&pluginv1.ApplyResponse{DerivedContract: &pluginv1.DerivedContract{
			SchemaId: "fake-source/outputs", Rev: "1", Schema: []byte(`{}`),
			Rung: pluginv1.DerivedContract_RUNG_TOOL_DERIVED,
		}},
		&pluginv1.ApplyResponse{DerivedContract: &pluginv1.DerivedContract{
			SchemaId: "someone-elses/outputs", Rev: "1", Schema: []byte(`{}`),
		}},
		terminal(true, "done"),
	)
	if len(res.Derived) != 1 || res.Derived[0].SchemaID != "fake-source/outputs" {
		t.Fatalf("only the plugin's own namespace may be declared into: %+v", res.Derived)
	}
	if len(res.Rejections) != 1 {
		t.Fatalf("the land-grab must be refused and surfaced: %+v", res.Rejections)
	}
}

// TestCheckpointSurvives: a gracefully-aborting Apply says where it stopped, and
// the core feeds that back as a resume token (invariant #7). Losing it turns a
// resumable abort into a full re-run.
func TestCheckpointSurvives(t *testing.T) {
	ev := event(pluginv1.TaskEvent_LEVEL_WARN, "aborting")
	ev.Checkpoint = "batch-3"
	res := govern(t, NewHost(testGrant()), nil,
		&pluginv1.ApplyResponse{Event: ev},
		terminal(false, "aborted on signal"),
	)
	if res.Checkpoint != "batch-3" {
		t.Fatalf("checkpoint lost: %q", res.Checkpoint)
	}
}

// TestWriterRefIsDerivedFromTheGrant: the provenance identity a plugin's writes
// would carry comes from the grant, and the plugin has no way to influence it
// (invariant #6). There is no setter, which is the actual assertion.
func TestWriterRefIsDerivedFromTheGrant(t *testing.T) {
	if got := testGrant().WriterRef(); got != "plugin/fake/fake-source/syncer" {
		t.Fatalf("WriterRef = %q", got)
	}
}
