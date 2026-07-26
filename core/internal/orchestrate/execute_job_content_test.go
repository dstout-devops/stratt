package orchestrate

import (
	"encoding/json"
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

// TestExecuteJobPlugin_MountsDeclaredContent pins ADR-0134 D2 at the seam that matters: the
// spine merges an Actuator's DECLARED content root into JobSpec.Files under project/, and it
// reads nothing tool-specific to do so.
//
// The negative half is the load-bearing one. Core must NOT resolve content from Step params
// — that was the obvious first design and it is exactly what ADR-0117 D3a exists to keep out
// of this path, because it puts `if ansible {}` into the tool-blind dispatcher (§1.4). So the
// Step here carries `params.playbook` (which the SHIM reads, downstream of the port) against
// an Actuator declaring NO content, and the assertion is that nothing gets mounted.
//
// Asserted over the Site transport for the same reason the image test is: the spec is built
// ONCE, before the local/remote branch, so capturing it there proves both paths.
func TestExecuteJobPlugin_MountsDeclaredContent(t *testing.T) {
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
			// Two declarations differing ONLY in their content root — the same shape as
			// the D3a image pair, one field along.
			"ansible": {Host: host, Grant: grant, JobCommand: []string{"stratt-ansible"}},
			"ansible-project": {
				Host: host, Grant: grant, JobCommand: []string{"stratt-ansible"},
				Content: map[string]string{
					"site.yml":                 "- hosts: all\n",
					"roles/common/tasks/x.yml": "- debug: {msg: hi}\n",
				},
			},
		}, nil),
	}
	resolved := ResolvedTargets{Targets: []actuators.Target{{EntityID: "e-web-2", Name: "web-2"}}}

	run := func(actuator string, params string) actuators.JobSpec {
		t.Helper()
		ts := &testsuite.WorkflowTestSuite{}
		env := ts.NewTestActivityEnvironment()
		env.RegisterActivity(a.Execute)
		in := RunInput{Actuator: actuator, Principal: "alice"}
		if params != "" {
			in.Params = json.RawMessage(params)
		}
		if _, err := env.ExecuteActivity(a.Execute, in, 3, "edge-1", resolved,
			[]dispatch.CredentialMount(nil)); err != nil {
			t.Fatalf("execute %s: %v", actuator, err)
		}
		return gw.gotReq.Spec
	}

	files := run("ansible-project", `{"playbook":"site.yml"}`).Files
	if got := files["project/site.yml"]; got != "- hosts: all\n" {
		t.Fatalf("the declared content root must mount under project/, got %q (files: %v)", got, keysOf(files))
	}
	// A WHOLE DIRECTORY, not one file: a real Ansible project has roles/ and group_vars/
	// and playbooks that import_playbook each other (D2).
	if _, ok := files["project/roles/common/tasks/x.yml"]; !ok {
		t.Fatalf("the whole tree must mount, not just the named playbook (files: %v)", keysOf(files))
	}
	// The sovereign ApplyRequest is still there — content is merged into Files, never over it.
	if _, ok := files["stratt/request.json"]; !ok {
		t.Fatalf("merging content must not displace the request (files: %v)", keysOf(files))
	}

	// The negative: same Step params, an Actuator declaring no content ⇒ nothing mounts.
	// If this ever fails, the spine has started reading a tool's params to find content.
	bare := run("ansible", `{"playbook":"site.yml"}`).Files
	if len(bare) != 1 {
		t.Fatalf("content must come from the ACTUATOR DECLARATION and never from Step params — an undeclared Actuator mounted %v", keysOf(bare))
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
