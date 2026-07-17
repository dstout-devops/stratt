package orchestrate

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	"github.com/dstout-devops/stratt/core/internal/siteproto"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// fakeSiteGateway plays canned typed governance frames to onResp — a Site that
// forwards a shim's ApplyResponses without folding (ADR-0051 MF2). It records the
// dispatched request so the test can assert the Typed marker + RemoteSafe Spec.
type fakeSiteGateway struct {
	frames []*pluginv1.ApplyResponse
	jobOK  bool
	gotReq siteproto.DispatchRequest
}

func (f *fakeSiteGateway) DispatchAndAwait(context.Context, siteproto.DispatchRequest, func()) (dispatch.Result, error) {
	return dispatch.Result{}, nil
}
func (f *fakeSiteGateway) Cancel(context.Context, string, string) error { return nil }

func (f *fakeSiteGateway) StreamApply(_ context.Context, req siteproto.DispatchRequest, _ func(), onResp func(json.RawMessage)) (bool, error) {
	f.gotReq = req
	for _, r := range f.frames {
		b, err := protojson.Marshal(r)
		if err != nil {
			return false, err
		}
		onResp(b)
	}
	return f.jobOK, nil
}

// TestExecuteJobPlugin_RemoteSiteGovernsHubSide proves ADR-0051 MF2: a Site-homed
// EE-Job Step runs the shim AT the Site and forwards its typed stdout Site→hub,
// where the SAME governor folds — the confused-deputy target is rejected, an
// ungranted facet is gated out, the resolved target's facts + status survive, and
// each governed target is stamped with its execution Site (§1.8 descent).
func TestExecuteJobPlugin_RemoteSiteGovernsHubSide(t *testing.T) {
	grant := pluginhost.Grant{
		PluginIdentity:  "ansible",
		Tier:            pluginhost.TierTrusted,
		Source:          types.Source{Kind: "ansible", Name: "ansible"},
		FacetNamespaces: []string{"os.kernel"},
		IdentitySchemes: []string{"host.name"},
	}
	host := pluginhost.New(nil, nil, grant, slog.New(slog.NewTextHandler(io.Discard, nil)))

	gw := &fakeSiteGateway{
		jobOK: true,
		frames: []*pluginv1.ApplyResponse{
			{Result: &pluginv1.ItemResult{ItemKey: "web-2", Status: pluginv1.ItemResult_STATUS_CHANGED}},
			// A per-target status for a host OUTSIDE the resolved set — confused deputy.
			{Result: &pluginv1.ItemResult{ItemKey: "intruder", Status: pluginv1.ItemResult_STATUS_FAILED}},
			// Facts write-back: os.kernel granted (survives), secret.leak ungranted (gated).
			{WriteBack: []*pluginv1.ObservedEntity{{
				Kind:         "host",
				IdentityKeys: map[string]string{"host.name": "web-2"},
				Facets: map[string][]byte{
					"os.kernel":   []byte(`{"family":"linux"}`),
					"secret.leak": []byte(`{"stolen":"creds"}`),
				},
			}}},
			{Event: &pluginv1.TaskEvent{Terminal: true, Ok: true}},
		},
	}
	a := &Activities{
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sites: gw,
		PluginActuators: map[string]PluginActuator{
			"ansible": {Host: host, DryRunnable: true, Grant: grant, JobCommand: []string{"stratt-ansible"}},
		},
	}
	resolved := ResolvedTargets{Targets: []actuators.Target{{EntityID: "e-web-2", Name: "web-2"}}}

	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)
	val, err := env.ExecuteActivity(a.Execute,
		// The Run scopes os.kernel into its write-back (ADR-0054); os.kernel then
		// survives grant∩scope while the ungranted secret.leak is gated regardless.
		RunInput{Actuator: "ansible", Principal: "alice", FacetWriteScope: []string{"os.kernel"}}, 3, "edge-1", resolved, []dispatch.CredentialMount(nil))
	if err != nil {
		t.Fatalf("execute EE-Job at Site: %v", err)
	}
	var res dispatch.Result
	if err := val.Get(&res); err != nil {
		t.Fatalf("decode result: %v", err)
	}

	// The dispatch crossed as a TYPED request with a RemoteSafe shim Spec.
	if !gw.gotReq.Typed {
		t.Fatal("a Site EE-Job Step must dispatch Typed=true (the Site forwards, folds nothing)")
	}
	if gw.gotReq.Spec.RemoteSafe() != nil {
		t.Fatalf("the shim JobSpec shipped to a Site must be RemoteSafe: %v", gw.gotReq.Spec.RemoteSafe())
	}
	// Governance ran hub-side over the Site-forwarded frames.
	if !res.Succeeded {
		t.Fatalf("terminal + jobOK + no in-set failure must fold Succeeded: %+v", res)
	}
	if res.PerTarget["web-2"] != "changed" {
		t.Fatalf("resolved target status must survive: %+v", res.PerTarget)
	}
	if _, leaked := res.PerTarget["intruder"]; leaked {
		t.Fatal("confused-deputy target must never enter the outcome over the Site transport")
	}
	if got := res.Facts["web-2"]["os.kernel"]; len(got) == 0 {
		t.Fatalf("granted facts must re-correlate to the resolved target: %+v", res.Facts)
	}
	if _, leaked := res.Facts["web-2"]["secret.leak"]; leaked {
		t.Fatal("ungranted facet must be gated out hub-side over the Site transport (MF3)")
	}
	if res.SiteByTarget["web-2"] != "edge-1" {
		t.Fatalf("governed target must be stamped with its execution Site (§1.8): %+v", res.SiteByTarget)
	}
}

// TestExecuteJobPlugin_RemoteSiteNoGatewayFailsVisibly proves a Site-homed EE-Job
// Step with no Site gateway fails VISIBLY, never silently hub-local (§1.8).
func TestExecuteJobPlugin_RemoteSiteNoGatewayFailsVisibly(t *testing.T) {
	grant := pluginhost.Grant{PluginIdentity: "ansible", Tier: pluginhost.TierTrusted,
		Source: types.Source{Kind: "ansible", Name: "ansible"}, IdentitySchemes: []string{"host.name"}}
	host := pluginhost.New(nil, nil, grant, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a := &Activities{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		PluginActuators: map[string]PluginActuator{
			"ansible": {Host: host, DryRunnable: true, Grant: grant, JobCommand: []string{"stratt-ansible"}},
		},
	}
	_, err := a.executeJobPlugin(context.Background(),
		RunInput{Actuator: "ansible"}, 0, "edge-1", ResolvedTargets{}, nil,
		a.PluginActuators["ansible"])
	if err == nil {
		t.Fatal("a Site EE-Job Step with no site gateway must fail visibly")
	}
}

// TestExecute_FacetWriteScopeAdmissionLint proves the ADR-0054 MF-2 admission
// lint: a declared FacetWriteScope with an entry OUTSIDE the actuator's registered
// facet grant fails the Run LOUDLY at launch (naming the offending namespace),
// rather than silently dropping the facet at govern (§1.8 — a mismatch is
// diagnosed, not a silent no-op). A scope wholly within grant is admitted.
func TestExecute_FacetWriteScopeAdmissionLint(t *testing.T) {
	grant := pluginhost.Grant{
		PluginIdentity:  "ansible",
		Tier:            pluginhost.TierTrusted,
		Source:          types.Source{Kind: "ansible", Name: "ansible"},
		FacetNamespaces: []string{"os.kernel"},
		IdentitySchemes: []string{"host.name"},
	}
	host := pluginhost.New(nil, nil, grant, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a := &Activities{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		PluginActuators: map[string]PluginActuator{
			"ansible": {Host: host, DryRunnable: true, Grant: grant, JobCommand: []string{"stratt-ansible"}},
		},
	}
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)

	// os.hardening.sshd is NOT in the ansible grant → the whole Run is rejected.
	_, err := env.ExecuteActivity(a.Execute,
		RunInput{Actuator: "ansible", Principal: "alice", FacetWriteScope: []string{"os.hardening.sshd"}},
		0, "", ResolvedTargets{Targets: []actuators.Target{{EntityID: "e-1", Name: "web-1"}}},
		[]dispatch.CredentialMount(nil))
	if err == nil {
		t.Fatal("a FacetWriteScope entry outside the actuator's grant must fail the Run at launch (ADR-0054 MF-2)")
	}
	if !strings.Contains(err.Error(), "os.hardening.sshd") || !strings.Contains(err.Error(), "registered grant") {
		t.Fatalf("the rejection must name the offending namespace + cause (§1.8): %v", err)
	}
}
