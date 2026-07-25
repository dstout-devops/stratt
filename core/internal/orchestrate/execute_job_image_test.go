package orchestrate

import (
	"io"
	"log/slog"
	"testing"

	"go.temporal.io/sdk/testsuite"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// TestExecuteJobPlugin_CarriesDeclaredImage pins ADR-0117 D3a: an EE-Job Actuator's
// DECLARED image must reach the JobSpec, because that is the whole mechanism for
// per-Step EE (and therefore content) selection.
//
// This was the gap behind the ADR's own correction: `executeJobPlugin` built
// JobSpec{Files, Command} with no Image, so every ansible Run silently got the global
// STRATT_EE_IMAGE no matter what was declared — while `params.eeImage` sat in the
// Contract looking honored. Only executeMCP set it. The fix is deliberately NOT to
// teach the core to read `params.eeImage`: that would be the `if ansible{}` §1.4
// forbids. A Step selects an Actuator; the Actuator declaration carries the image.
//
// Asserted over the Site transport because the spec is built ONCE, before the
// local/remote branch — so capturing it there proves it for both paths, and reuses the
// fakeSiteGateway rather than inventing a second fake.
func TestExecuteJobPlugin_CarriesDeclaredImage(t *testing.T) {
	const declaredImage = "stratt-ee-crypto:dev"

	grant := pluginhost.Grant{
		PluginIdentity: "ansible", Tier: pluginhost.TierTrusted,
		Source: types.Source{Kind: "ansible", Name: "ansible"}, IdentitySchemes: []string{"host.name"},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	host := pluginhost.New(nil, nil, grant, log)
	gw := &fakeSiteGateway{jobOK: true, frames: []*pluginv1.ApplyResponse{
		{Result: &pluginv1.ItemResult{ItemKey: "web-2", Status: pluginv1.ItemResult_STATUS_OK}},
		{Event: &pluginv1.TaskEvent{Terminal: true, Ok: true}},
	}}
	a := &Activities{
		Log:   log,
		Sites: gw,
		Plugins: NewPluginRegistryWith(map[string]PluginActuator{
			// Two declarations differing ONLY in their image — the D3a shape.
			"ansible": {Host: host, Grant: grant, JobCommand: []string{"stratt-ansible"}},
			"ansible-crypto": {
				Host: host, Grant: grant, JobCommand: []string{"stratt-ansible"},
				Image: declaredImage,
			},
		}, nil),
	}
	resolved := ResolvedTargets{Targets: []actuators.Target{{EntityID: "e-web-2", Name: "web-2"}}}

	run := func(actuator string) actuators.JobSpec {
		t.Helper()
		ts := &testsuite.WorkflowTestSuite{}
		env := ts.NewTestActivityEnvironment()
		env.RegisterActivity(a.Execute)
		if _, err := env.ExecuteActivity(a.Execute,
			RunInput{Actuator: actuator, Principal: "alice"}, 3, "edge-1", resolved,
			[]dispatch.CredentialMount(nil)); err != nil {
			t.Fatalf("execute %s: %v", actuator, err)
		}
		return gw.gotReq.Spec
	}

	if got := run("ansible-crypto").Image; got != declaredImage {
		t.Fatalf("declared image did not reach the JobSpec: got %q, want %q — per-Step EE selection is inert", got, declaredImage)
	}
	// The undeclared sibling must stay on the dispatcher default (empty ⇒ fall back),
	// so adding this field changes nothing for every already-shipped Actuator.
	if got := run("ansible").Image; got != "" {
		t.Fatalf("an Actuator declaring no image must leave JobSpec.Image empty for the dispatcher default, got %q", got)
	}
}
