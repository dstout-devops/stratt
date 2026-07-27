package mockstratt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// The EE-Job (subprocess) transport, driven against a real child process. These
// tests are the ones that would have to be a kind cluster otherwise, which is the
// whole claim of ADR-0137 D6.

func run(t *testing.T, s *Subprocess, h *Host, req Request) Result {
	t.Helper()
	res, err := s.Run(context.Background(), h, req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

func targets(names ...string) []ApplyTarget {
	out := make([]ApplyTarget, 0, len(names))
	for _, n := range names {
		out = append(out, ApplyTarget{Name: n, Address: n + ".example.com"})
	}
	return out
}

// TestSubprocess_HappyPath: the transport end to end — stage the request, spawn,
// decode frames, govern, fold.
func TestSubprocess_HappyPath(t *testing.T) {
	res := run(t, fake(t, "ok"), NewHost(testGrant()), Request{
		Params:  []byte(`{"playbook":"site.yml"}`),
		Targets: targets("web-1", "web-2"),
	})
	if !res.Succeeded {
		t.Fatalf("expected success: %+v", res)
	}
	if res.PerTarget["web-1"] != "changed" || res.PerTarget["web-2"] != "changed" {
		t.Fatalf("per-target: %+v", res.PerTarget)
	}
}

// TestSubprocess_StagesTheSovereignRequest is the fidelity check: the plugin
// receives the SAME ApplyRequest shape the core sends, with the content root
// mounted at project/. If this drifts, every plugin developed here is developed
// against a fiction.
func TestSubprocess_StagesTheSovereignRequest(t *testing.T) {
	dir := t.TempDir()
	s := fake(t, "echo")
	s.Dir = dir
	s.DryRunnable = true

	res := run(t, s, NewHost(testGrant()), Request{
		Params:  []byte(`{"playbook":"site.yml"}`),
		Targets: targets("web-1"),
		DryRun:  true,
		Creds:   []Credential{{Name: "ssh-key"}},
		Content: map[string]string{
			"site.yml":                 "- hosts: all\n",
			"roles/common/tasks/x.yml": "- debug: {}\n",
		},
	})
	if !res.Succeeded {
		t.Fatalf("echo scenario failed: %+v", res)
	}

	// The plugin must find its request exactly where the EE-Job transport puts it.
	raw, err := os.ReadFile(filepath.Join(dir, "stratt", "request.json"))
	if err != nil {
		t.Fatalf("the plugin must find its request at stratt/request.json: %v", err)
	}
	// Decoded as the sovereign message, not string-matched: `desired` is a proto
	// bytes field and therefore BASE64 on the wire. Grepping the raw JSON for the
	// params would pass or fail for reasons that have nothing to do with the port.
	var got pluginv1.ApplyRequest
	if err := protojson.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the staged request must be a decodable ApplyRequest: %v", err)
	}
	if string(got.GetDesired().GetBytes()) != `{"playbook":"site.yml"}` {
		t.Errorf("params must cross opaque and intact, got %q", got.GetDesired().GetBytes())
	}
	if !got.GetDryRun() {
		t.Error("the check-mode bit must cross (MF6)")
	}
	if n := len(got.GetTargets()); n != 1 || got.GetTargets()[0].GetName() != "web-1" {
		t.Fatalf("targets must cross legibly, never baked into params: %+v", got.GetTargets())
	}
	if addr := got.GetTargets()[0].GetAddress(); addr != "web-1.example.com" {
		t.Errorf("mgmt address must cross as a first-class field, not a tool var: %q", addr)
	}
	if ids := got.GetTargets()[0].GetIdentityKeys(); ids["host.name"] != "web-1" {
		t.Errorf("identity keys re-correlate write-back to the Entity; got %+v", ids)
	}
	// §2.5: a name crosses, material never does. The absence is the assertion.
	creds := got.GetEnvelope().GetCreds()
	if len(creds) != 1 || creds[0].GetName() != "ssh-key" {
		t.Fatalf("credential NAMES must cross: %+v", creds)
	}

	// And the declared content root is mounted at project/, tree intact.
	for rel, want := range map[string]string{
		"site.yml":                 "- hosts: all\n",
		"roles/common/tasks/x.yml": "- debug: {}\n",
	} {
		b, err := os.ReadFile(filepath.Join(dir, "project", filepath.FromSlash(rel)))
		if err != nil || string(b) != want {
			t.Errorf("content root must mount at project/%s: %v %q", rel, err, b)
		}
	}
}

// TestSubprocess_ReadOnlyProject reproduces the mount ADR-0134 had to design
// around: project/ arrives read-only, so a plugin laying a file over it fails.
// This is the fidelity that stops "passes on my laptop, EACCES in the pod".
func TestSubprocess_ReadOnlyProject(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores the mode bit — the check is a strong hint, not a guarantee")
	}
	s := fake(t, "writes-into-project")
	s.ReadOnlyProject = true
	res := run(t, s, NewHost(testGrant()), Request{
		Targets: targets("web-1"),
		Content: map[string]string{"site.yml": "- hosts: all\n"},
	})
	if res.Succeeded {
		t.Fatal("writing into a read-only project/ must fail here, exactly as it would against a ConfigMap mount")
	}

	// And the same plugin passes when the root is writable — which is precisely
	// why the flag has to exist rather than being assumed.
	s2 := fake(t, "writes-into-project")
	if res2 := run(t, s2, NewHost(testGrant()), Request{
		Targets: targets("web-1"),
		Content: map[string]string{"site.yml": "- hosts: all\n"},
	}); !res2.Succeeded {
		t.Fatalf("a writable project/ must still permit the write: %+v", res2)
	}
}

// TestSubprocess_GreenTerminalThenCrash: both signals are folded. A plugin that
// says it worked and then dies must read NOT-OK — the OOMKill case.
func TestSubprocess_GreenTerminalThenCrash(t *testing.T) {
	res := run(t, fake(t, "green-then-crash"), NewHost(testGrant()), Request{Targets: targets("web-1")})
	if res.Succeeded {
		t.Fatal("a green terminal followed by a non-zero exit must read NOT-OK (OOMKill, torn cleanup, serialize failure)")
	}
	if res.Error == "" {
		t.Error("the exit status must be reported when the plugin said nothing better")
	}
}

// TestSubprocess_LoudFailureKeepsTheReason: the plugin's own account beats an exit
// code. Getting this backwards replaces "no route to host" with "exit status 1".
func TestSubprocess_LoudFailureKeepsTheReason(t *testing.T) {
	res := run(t, fake(t, "loud-failure"), NewHost(testGrant()), Request{Targets: targets("web-1")})
	if res.Succeeded {
		t.Fatal("expected failure")
	}
	if !strings.Contains(res.Error, "no route to host") {
		t.Fatalf("the plugin's own reason must win over the exit code, got %q", res.Error)
	}
	if res.PerTarget["web-1"] != "unreachable" {
		t.Errorf("per-target: %+v", res.PerTarget)
	}
}

// TestSubprocess_NonFrameOutputIsDiagnostic: banners and broken lines are routed
// to diagnostics, not fed to the governor and not silently dropped. A plugin that
// dies before its first frame has nothing else to say for itself (§1.8).
func TestSubprocess_NonFrameOutputIsDiagnostic(t *testing.T) {
	res := run(t, fake(t, "noisy"), NewHost(testGrant()), Request{Targets: targets("web-1")})
	if !res.Succeeded {
		t.Fatalf("stray output must not break the run: %+v", res)
	}
	joined := strings.Join(res.Diagnostics, "\n")
	for _, want := range []string{"PLAY [all]", "this is not valid json", "something on stderr"} {
		if !strings.Contains(joined, want) {
			t.Errorf("diagnostics lost %q: %v", want, res.Diagnostics)
		}
	}
}

// TestSubprocess_StderrIsNotAFrameChannel: in a pod only stdout carries port
// frames. Merging the streams here would let a plugin pass locally by writing
// frames the real dispatcher never reads.
func TestSubprocess_StderrIsNotAFrameChannel(t *testing.T) {
	res := run(t, fake(t, "noisy"), NewHost(testGrant()), Request{Targets: targets("web-1")})
	for _, d := range res.Diagnostics {
		if strings.HasPrefix(d, "stderr: ") && strings.Contains(d, "something on stderr") {
			return
		}
	}
	t.Fatalf("stderr must be retained as diagnostics and never parsed for frames: %v", res.Diagnostics)
}

// TestSubprocess_DryRunRefusedBeforeSpawn: MF6, core-side. A shim that silently
// ignored the check bit would run live side effects, so the refusal happens
// BEFORE the process starts rather than being trusted to the plugin.
func TestSubprocess_DryRunRefusedBeforeSpawn(t *testing.T) {
	s := fake(t, "ok") // DryRunnable defaults false, as an undeclared Actuator does
	_, err := s.Run(context.Background(), NewHost(testGrant()), Request{DryRun: true, Targets: targets("web-1")})
	if err == nil {
		t.Fatal("a dry-run against an actuator that does not declare dryRunnable must be refused core-side, before spawn")
	}
	s.DryRunnable = true
	if _, err := s.Run(context.Background(), NewHost(testGrant()), Request{DryRun: true, Targets: targets("web-1")}); err != nil {
		t.Fatalf("a declared dry-runnable actuator must be allowed: %v", err)
	}
}

// TestSubprocess_ContentPathContainment: the harness refuses to stage outside
// project/. This is a containment check on the harness's own filesystem, not an
// opinion about content — the core never inspects what a contentDir holds.
func TestSubprocess_ContentPathContainment(t *testing.T) {
	_, err := fake(t, "ok").Run(context.Background(), NewHost(testGrant()), Request{
		Targets: targets("web-1"),
		Content: map[string]string{"../escape.yml": "nope"},
	})
	if err == nil || !strings.Contains(err.Error(), "escapes project/") {
		t.Fatalf("a content path escaping project/ must be refused: %v", err)
	}
}
