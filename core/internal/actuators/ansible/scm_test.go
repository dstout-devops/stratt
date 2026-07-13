package ansible

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/actuators"
)

func prep(t *testing.T, raw string) (actuators.JobSpec, error) {
	t.Helper()
	return Actuator{}.Prepare(json.RawMessage(raw), []actuators.Target{{Name: "h1"}})
}

func TestPrepareSCMClonesInPod(t *testing.T) {
	spec, err := prep(t, `{"scm":{"repo":"https://x/r.git","ref":"main","playbook":"site.yml"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := spec.Files["clone.sh"]; !ok {
		t.Fatal("SCM Step must mount clone.sh")
	}
	if _, ok := spec.Files["project/play.yml"]; ok {
		t.Fatal("SCM Step must NOT write an inline play")
	}
	if _, ok := spec.Files["inventory/hosts"]; !ok {
		t.Fatal("SCM Step must still render inventory/hosts from the View")
	}
	if got := strings.Join(spec.Command, " "); got != "sh /runner/clone.sh" {
		t.Fatalf("command: %q", got)
	}
	if spec.Env["SCM_REPO"] != "https://x/r.git" || spec.Env["SCM_PLAYBOOK"] != "site.yml" || spec.Env["SCM_REF"] != "main" {
		t.Fatalf("env: %+v", spec.Env)
	}
	if _, ok := spec.Env["SCM_CHECK"]; ok {
		t.Fatal("SCM_CHECK must be unset when check is false")
	}
}

func TestPrepareSCMCheckMode(t *testing.T) {
	spec, err := prep(t, `{"scm":{"repo":"https://x/r.git","playbook":"p.yml"},"check":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Env["SCM_CHECK"] != "1" {
		t.Fatalf("check mode must set SCM_CHECK=1: %+v", spec.Env)
	}
}

func TestPlayAndSCMMutuallyExclusive(t *testing.T) {
	if _, err := prep(t, `{"play":"- hosts: all\n","scm":{"repo":"r","playbook":"p"}}`); err == nil {
		t.Fatal("play and scm together must be rejected")
	}
}

func TestValidateSCMRejectsBadPaths(t *testing.T) {
	for _, bad := range []string{
		`{"scm":{"repo":"r","playbook":"../etc/passwd"}}`,
		`{"scm":{"repo":"r","playbook":"/abs/site.yml"}}`,
		`{"scm":{"repo":"","playbook":"site.yml"}}`,
		`{"scm":{"repo":"r","playbook":""}}`,
		// git argument injection: a repo/ref parsed as an option, not a URL.
		`{"scm":{"repo":"--upload-pack=touch /tmp/x","playbook":"site.yml"}}`,
		`{"scm":{"repo":"https://x/r.git","ref":"--output=/etc/x","playbook":"site.yml"}}`,
	} {
		if _, err := prep(t, bad); err == nil {
			t.Errorf("expected rejection for %s", bad)
		}
	}
}

func TestExtraVarsWrittenBothPaths(t *testing.T) {
	// play path
	spec, err := prep(t, `{"play":"- hosts: all\n","extraVars":{"msg":"hello","n":3}}`)
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := spec.Files["env/extravars"]
	if !ok || !strings.Contains(ev, `"msg":"hello"`) || !strings.Contains(ev, `"n":3`) {
		t.Fatalf("play path env/extravars: %q", ev)
	}
	// scm path
	spec, err = prep(t, `{"scm":{"repo":"https://x/r.git","playbook":"p.yml"},"extraVars":{"msg":"hi"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if ev := spec.Files["env/extravars"]; !strings.Contains(ev, `"msg":"hi"`) {
		t.Fatalf("scm path env/extravars: %q", ev)
	}
	// absent when no extra vars
	spec, _ = prep(t, `{"play":"- hosts: all\n"}`)
	if _, ok := spec.Files["env/extravars"]; ok {
		t.Fatal("env/extravars must be absent when no extraVars given")
	}
}

func TestCloneScriptSeparatesOptionsFromArgs(t *testing.T) {
	// The "--" separator is the exec-time defense against argument injection.
	if !strings.Contains(cloneScript, ` -- "$SCM_REPO"`) {
		t.Fatalf("clone script must place -- before the repo positional:\n%s", cloneScript)
	}
}
