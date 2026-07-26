package ansible_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/sdk/mockstratt"
)

// Port conformance for the ansible plugin, driven through mock-stratt (ADR-0137 D6).
//
// WHAT MAKES THIS DIFFERENT FROM EVERY OTHER TEST IN THIS PACKAGE. The rest call
// Run() in-process with a fake commandRunner — they test the shim's logic. This
// builds the ACTUAL cmd/stratt-ansible binary and drives it the way the EE-Job
// transport does: a request staged on disk, a real spawn, proto-JSON frames over a
// real pipe, and the same governance the hub applies. Everything between main()
// and the governor is exercised, which is precisely the span the in-process tests
// cannot reach — and where ADR-0134's mount, ADR-0117's terminal fold, and the
// confused-deputy gate all live.
//
// It needs no cluster, no Postgres, no Temporal and no ansible: `ansible-runner`
// is a subprocess the shim spawns by name (STRATT_ANSIBLE_RUNNER), so a fake one
// standing in for it keeps this a test of the PORT rather than of ansible.

// buildShim compiles cmd/stratt-ansible once per run.
func buildShim(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a binary; skipped under -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain available to build the shim")
	}
	bin := filepath.Join(t.TempDir(), "stratt-ansible")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/stratt-ansible")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build shim: %v\n%s", err, out)
	}
	return bin
}

// fakeRunner writes a stand-in for ansible-runner emitting the `-j` event lines
// the shim parses. The shim spawns it by name, so this is the same seam the GPLv3
// boundary is drawn at (charter §3) — which is what lets the port be tested
// without ansible present at all.
func fakeRunner(t *testing.T, rc int, lines ...string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake runner is a shell script")
	}
	path := filepath.Join(t.TempDir(), "ansible-runner")
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	for _, l := range lines {
		fmt.Fprintf(&b, "cat <<'STRATT_EOF'\n%s\nSTRATT_EOF\n", l)
	}
	fmt.Fprintf(&b, "exit %d\n", rc)
	if err := os.WriteFile(path, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func onOK(host string, changed bool) string {
	return fmt.Sprintf(`{"event":"runner_on_ok","event_data":{"host":%q,"res":{"changed":%t}}}`, host, changed)
}

func onUnreachable(host, msg string) string {
	return fmt.Sprintf(`{"event":"runner_on_unreachable","event_data":{"host":%q,"res":{"msg":%q}}}`, host, msg)
}

// grant mirrors estate/actuators/ansible-platform-baseline.yaml — the declaration
// this plugin actually ships under. Testing against an invented grant would prove
// conformance to a ceiling nobody deploys.
func grant() mockstratt.Grant {
	return mockstratt.Grant{
		PluginIdentity:  "ansible",
		Tier:            mockstratt.TierTrusted,
		SourceName:      "ansible",
		FacetNamespaces: []string{"os.kernel", "app.config"},
		IdentitySchemes: []string{"host.name"},
	}
}

func host(t *testing.T) *mockstratt.Host {
	t.Helper()
	return mockstratt.NewHost(grant()).WithFacetWriteScope("os.kernel", "app.config")
}

func subproc(t *testing.T, bin, runner string) *mockstratt.Subprocess {
	t.Helper()
	return &mockstratt.Subprocess{
		Binary:      bin,
		Env:         []string{"STRATT_ANSIBLE_RUNNER=" + runner},
		DryRunnable: true,
		// project/ arrives as a read-only ConfigMap mount. ADR-0134 had to reserve
		// the name play.yml because of it, so a harness that mounted it writable
		// would test the one arrangement production never uses.
		ReadOnlyProject: true,
	}
}

// TestConformance_MountedProject is ADR-0134 end to end through the real binary:
// the core mounts a content root at project/, the Step names a file within it, and
// the shim runs THAT file without writing into the tree.
func TestConformance_MountedProject(t *testing.T) {
	bin := buildShim(t)
	runner := fakeRunner(t, 0, onOK("web-1", true), onOK("web-2", false))

	req := mockstratt.Request{
		Params:  []byte(`{"playbook":"site.yml"}`),
		Targets: []mockstratt.ApplyTarget{{Name: "web-1", Address: "10.0.0.1"}, {Name: "web-2", Address: "10.0.0.2"}},
		Content: map[string]string{
			"site.yml":                    "- hosts: all\n  tasks: []\n",
			"roles/common/tasks/main.yml": "- debug: {msg: hi}\n",
		},
	}
	res, err := subproc(t, bin, runner).Run(context.Background(), host(t), req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Succeeded {
		t.Fatalf("expected success: %+v", res)
	}
	if res.PerTarget["web-1"] != "changed" || res.PerTarget["web-2"] != "ok" {
		t.Fatalf("per-target: %+v (diagnostics: %v)", res.PerTarget, res.Diagnostics)
	}

	c := mockstratt.Conformance{Request: req, Result: res}
	if vs := c.Errors(); len(vs) != 0 {
		t.Fatalf("the shipped plugin must pass port conformance:\n%s", c.Report())
	}
}

// TestConformance_MissingPlaybookNamesTheMount: the mount silently not happening
// must not surface as ansible's own "playbook could not be found", which names
// neither the Actuator nor the fact that a mount is involved (§1.8).
func TestConformance_MissingPlaybookNamesTheMount(t *testing.T) {
	bin := buildShim(t)
	runner := fakeRunner(t, 0)

	req := mockstratt.Request{
		Params:  []byte(`{"playbook":"absent.yml"}`),
		Targets: []mockstratt.ApplyTarget{{Name: "web-1", Address: "10.0.0.1"}},
		Content: map[string]string{"site.yml": "- hosts: all\n"},
	}
	res, err := subproc(t, bin, runner).Run(context.Background(), host(t), req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Succeeded {
		t.Fatal("a playbook absent from the mounted tree must fail")
	}
	if !strings.Contains(res.Error, "mounted project tree") {
		t.Fatalf("the failure must name the MOUNT, not just the missing file: %q", res.Error)
	}
	// And it is a conforming failure: it said why.
	c := mockstratt.Conformance{Request: req, Result: res}
	for _, v := range c.Errors() {
		if v.Check == "failure-states-cause" {
			t.Errorf("a stated cause must satisfy the check:\n%s", c.Report())
		}
	}
}

// TestConformance_VacuousRunIsRefused: ansible exits 0 when a play's pattern
// matches no host, so the Run would fold green having changed nothing. The plugin
// counts actuation itself — only the content-expertise knows a play can no-op —
// and the harness proves the refusal survives all the way to the governed verdict.
func TestConformance_VacuousRunIsRefused(t *testing.T) {
	bin := buildShim(t)
	runner := fakeRunner(t, 0) // rc=0, and not one per-host event

	req := mockstratt.Request{
		Params:  []byte(`{"playbook":"site.yml"}`),
		Targets: []mockstratt.ApplyTarget{{Name: "web-1", Address: "10.0.0.1"}},
		Content: map[string]string{"site.yml": "- hosts: nothing\n"},
	}
	res, err := subproc(t, bin, runner).Run(context.Background(), host(t), req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Succeeded {
		t.Fatal("a run that actuated no target must not read green, even at rc=0")
	}
}

// TestConformance_ConfusedDeputyIsGated: ansible's implicit localhost is absent
// from the rendered inventory, so results keyed to it are outside the resolved
// set. The hub refuses them — and the run must not pass conformance by counting
// them as work done.
func TestConformance_ConfusedDeputyIsGated(t *testing.T) {
	bin := buildShim(t)
	runner := fakeRunner(t, 0, onOK("localhost", true))

	req := mockstratt.Request{
		Params:  []byte(`{"playbook":"site.yml"}`),
		Targets: []mockstratt.ApplyTarget{{Name: "web-1", Address: "10.0.0.1"}},
		Content: map[string]string{"site.yml": "- hosts: localhost\n"},
	}
	res, err := subproc(t, bin, runner).Run(context.Background(), host(t), req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, ok := res.PerTarget["localhost"]; ok {
		t.Fatal("a result for a target outside the resolved set must be refused")
	}
	if res.Succeeded {
		t.Fatal("actuating only an unresolved target is a vacuous run, not a success")
	}
}

// TestConformance_UnreachableCarriesItsReason: ADR-0117 D5c from the plugin's
// side. The pod log is deleted with the Job, so a failure whose cause lives only
// there is a dead end.
func TestConformance_UnreachableCarriesItsReason(t *testing.T) {
	bin := buildShim(t)
	runner := fakeRunner(t, 4, onUnreachable("web-1", "ssh: no route to host"))

	req := mockstratt.Request{
		Params:  []byte(`{"playbook":"site.yml"}`),
		Targets: []mockstratt.ApplyTarget{{Name: "web-1", Address: "10.0.0.1"}},
		Content: map[string]string{"site.yml": "- hosts: all\n"},
	}
	res, err := subproc(t, bin, runner).Run(context.Background(), host(t), req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Succeeded {
		t.Fatal("an unreachable host must fail the run")
	}
	if res.PerTarget["web-1"] != "unreachable" {
		t.Fatalf("per-target: %+v", res.PerTarget)
	}
	// The reason rides the TYPED event channel, not the terminal — the terminal
	// carries the summary ("ansible-runner rc=4"). Asserting on the descent stream
	// rather than on res.Error is the point: that channel is what §1.8 descent
	// renders, and a harness that only kept the terminal would hide it.
	if !strings.Contains(res.Log(), "no route to host") {
		t.Errorf("ansible's own reason must survive to the descent stream:\n%s", res.Log())
	}
}

// TestConformance_InlinePlayStillWorks: the Actuator that declares no contentDir
// is untouched by ADR-0134 — the shim writes project/play.yml itself. This is the
// regression half, and it is also why play.yml is a RESERVED name in a mounted
// tree: the two paths would otherwise collide on a read-only mount.
func TestConformance_InlinePlayStillWorks(t *testing.T) {
	bin := buildShim(t)
	runner := fakeRunner(t, 0, onOK("web-1", false))

	req := mockstratt.Request{
		Params:  []byte(`{"play":"- hosts: all\n  tasks: []\n"}`),
		Targets: []mockstratt.ApplyTarget{{Name: "web-1", Address: "10.0.0.1"}},
		// No Content: nothing mounts, so project/ is the shim's to write.
	}
	s := subproc(t, bin, runner)
	s.ReadOnlyProject = false // no mount to be read-only
	res, err := s.Run(context.Background(), host(t), req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Succeeded {
		t.Fatalf("an Actuator declaring no contentDir must be unaffected: %+v", res)
	}
}
