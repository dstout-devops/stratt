package ansible

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The mounted-project content source (ADR-0134). The core mounts an Actuator's declared
// content root at project/ and the Step names a file within it; the shim's job is to run
// that file and to leave project/ alone — it does not fetch the content and does not know
// where it came from.

// mountProject writes files into <dir>/project, standing in for what the dispatcher's
// ConfigMap volume mounts do in a real pod.
func mountProject(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(dir, "project", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestShim_MountedPlaybook is the new branch's whole behaviour: run the named file out of
// the already-populated project/, and write nothing into it. That "write nothing" half is
// the reason this reuses the SCM branch rather than the inline one — project/ arrives
// mounted read-only, so laying an inline play.yml over it would fail in a pod.
func TestShim_MountedPlaybook(t *testing.T) {
	dir := t.TempDir()
	mountProject(t, dir, map[string]string{
		"site.yml":                 "- hosts: all\n  tasks: []\n",
		"roles/common/tasks/x.yml": "- debug: {msg: hi}\n",
	})
	req := Request{
		Params:  withDeclaredSSH(t, `{"playbook":"site.yml","extraVars":{"k":"v"}}`),
		Targets: []Target{{Name: "web-1"}},
	}
	run := &captureRunner{rc: 0}
	var buf bytes.Buffer
	if err := Run(context.Background(), &buf, dir, req, run, noClone(t)); err != nil {
		t.Fatalf("run: %v", err)
	}

	var sawPlaybook bool
	for i, a := range run.args {
		if a == "-p" && i+1 < len(run.args) && run.args[i+1] == "site.yml" {
			sawPlaybook = true
		}
	}
	if !sawPlaybook {
		t.Fatalf("ansible-runner must target the mounted playbook, args=%v", run.args)
	}
	// The source-independent parts are still laid out (inventory, extravars) — this
	// branch differs from the inline one only in NOT authoring project/.
	if _, err := os.Stat(filepath.Join(dir, "inventory", "hosts")); err != nil {
		t.Fatalf("inventory must still be rendered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "env", "extravars")); err != nil {
		t.Fatalf("extraVars must still be written: %v", err)
	}
	// Nothing was written over the mount.
	if _, err := os.Stat(filepath.Join(dir, "project", "play.yml")); !os.IsNotExist(err) {
		t.Fatalf("the mounted-project path must not author project/play.yml (stat err=%v)", err)
	}
	// The rest of the tree is untouched and available — roles/ and group_vars/ are why a
	// whole directory mounts rather than one file.
	if _, err := os.Stat(filepath.Join(dir, "project", "roles/common/tasks/x.yml")); err != nil {
		t.Fatalf("the mounted tree must survive intact: %v", err)
	}
}

// TestShim_MountedPlaybookRefusals covers what fails BEFORE ansible-runner is spawned. The
// existence check is a deliberate duplicate of the estate-load one: the load knows what the
// declaration said, this knows what the pod actually received, and a mount that silently did
// not happen would otherwise surface as ansible-runner's own "playbook could not be found",
// which names neither the Actuator nor the fact that a mount is involved (§1.8).
func TestShim_MountedPlaybookRefusals(t *testing.T) {
	cases := []struct {
		name   string
		params string
		want   string
	}{
		{"traversal", `{"playbook":"../../etc/passwd"}`, "relative path"},
		{"absolute", `{"playbook":"/etc/passwd"}`, "relative path"},
		{"leading dash", `{"playbook":"-oProxyCommand=x"}`, "must not begin with '-'"},
		{"not mounted", `{"playbook":"absent.yml"}`, "not in the mounted project tree"},
		{
			"scm and playbook together",
			`{"playbook":"site.yml","scm":{"repo":"https://git/x.git","playbook":"p.yml"}}`,
			"mutually exclusive",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mountProject(t, dir, map[string]string{"site.yml": "- hosts: all\n"})
			req := Request{Params: withDeclaredSSH(t, tc.params), Targets: []Target{{Name: "h"}}}
			var buf bytes.Buffer
			run := &captureRunner{rc: 0}
			if err := Run(context.Background(), &buf, dir, req, run, noClone(t)); err != nil {
				t.Fatalf("run: %v", err)
			}
			if len(run.args) != 0 {
				t.Fatalf("ansible-runner must not be spawned for a refused request, args=%v", run.args)
			}
			if !strings.Contains(buf.String(), tc.want) {
				t.Fatalf("emitted stream does not carry %q:\n%s", tc.want, buf.String())
			}
		})
	}
}

// TestShim_InlinePlayStillWorksBesideAMount pins D4's kept exception. `params.play` is not
// deprecated, and an Actuator may serve both — a project for the Workflows that need one,
// a short inline guard play for the one that does not. The two coexist because play.yml is
// a reserved name inside a content root, so the inline write can never land on a mount.
func TestShim_InlinePlayStillWorksBesideAMount(t *testing.T) {
	dir := t.TempDir()
	mountProject(t, dir, map[string]string{"site.yml": "- hosts: all\n"})
	req := Request{
		Params:  withDeclaredSSH(t, `{"play":"- hosts: nothing\n  tasks: []\n"}`),
		Targets: []Target{{Name: "h"}},
	}
	run := &captureRunner{rc: 0}
	var buf bytes.Buffer
	if err := Run(context.Background(), &buf, dir, req, run, noClone(t)); err != nil {
		t.Fatalf("run: %v", err)
	}
	var sawPlay bool
	for i, a := range run.args {
		if a == "-p" && i+1 < len(run.args) && run.args[i+1] == "play.yml" {
			sawPlay = true
		}
	}
	if !sawPlay {
		t.Fatalf("an inline play must still run from project/play.yml, args=%v", run.args)
	}
	got, err := os.ReadFile(filepath.Join(dir, "project", "play.yml"))
	if err != nil || !strings.Contains(string(got), "hosts: nothing") {
		t.Fatalf("the inline play must be written beside the mount: %v %q", err, got)
	}
}
