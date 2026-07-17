package ansible

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// fakeRunner stands in for ansible-runner — it emits canned -json lines, so the
// shim's interpretation is exercised with no ansible-runner (subprocess-only, §3).
type fakeRunner struct {
	lines []string
	rc    int
}

func (f fakeRunner) run(_ context.Context, _ string, _ []string, onLine func([]byte)) (int, error) {
	for _, l := range f.lines {
		onLine([]byte(l))
	}
	return f.rc, nil
}

// noClone fails if invoked — a non-SCM Run must never reach the cloner.
func noClone(t *testing.T) cloner {
	return func(context.Context, string, scmParams) error {
		t.Fatal("cloner invoked for a non-SCM request")
		return nil
	}
}

func runShim(t *testing.T, req Request, f fakeRunner) []*pluginv1.ApplyResponse {
	t.Helper()
	var buf bytes.Buffer
	if err := Run(context.Background(), &buf, t.TempDir(), req, f, noClone(t)); err != nil {
		t.Fatalf("run: %v", err)
	}
	var out []*pluginv1.ApplyResponse
	sc := bufio.NewScanner(&buf)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		r := &pluginv1.ApplyResponse{}
		if err := protojson.Unmarshal(sc.Bytes(), r); err != nil {
			t.Fatalf("emitted line is not a decodable ApplyResponse: %v\n%s", err, sc.Bytes())
		}
		out = append(out, r)
	}
	return out
}

// TestShim_FanOutFactsDriftDiagnostics proves the shim maps ansible-runner -json onto
// the port's typed shapes (ADR-0051): per-host ItemResult fan-out (the key new
// thing), facts write-back keyed by the TARGET's identity (MF4), check-mode drift
// (paths only), a non-event line as a diagnostic (MF5), and a required terminal.
func TestShim_FanOutFactsDriftDiagnostics(t *testing.T) {
	req := Request{
		Params: json.RawMessage(`{"play":"- hosts: all\n  tasks: []\n"}`),
		Targets: []Target{
			{Name: "web-1"},
			{Name: "web-2", Identity: map[string]string{"host.name": "web-2"}},
			{Name: "web-3"},
			{Name: "web-4"},
		},
	}
	f := fakeRunner{rc: 1, lines: []string{
		`PLAY [all] ****************`, // non-event banner → diagnostic (MF5)
		`{"event":"runner_on_ok","counter":1,"event_data":{"host":"web-1","task":"motd","res":{"changed":true,"diff":[{"after_header":"/etc/motd"}]}}}`,
		`{"event":"runner_on_ok","counter":2,"event_data":{"host":"web-2","res":{"ansible_facts":{"ansible_system":"Linux","ansible_kernel":"6.1.0","ansible_architecture":"x86_64"}}}}`,
		`{"event":"runner_on_failed","counter":3,"event_data":{"host":"web-3"}}`,
		`{"event":"runner_on_unreachable","counter":4,"event_data":{"host":"web-4"}}`,
	}}
	resps := runShim(t, req, f)

	perHost := map[string]pluginv1.ItemResult_Status{}
	var diagnostics, terminals, driftHosts, writeBacks int
	var kernelEntity *pluginv1.ObservedEntity
	var termOk bool
	for _, r := range resps {
		if res := r.GetResult(); res != nil {
			perHost[res.GetItemKey()] = res.GetStatus()
		}
		if ev := r.GetEvent(); ev != nil {
			if ev.GetFields()["kind"] == "diagnostic" {
				diagnostics++
			}
			if ev.GetTerminal() {
				terminals++
				termOk = ev.GetOk()
			}
		}
		if r.GetDrift() != nil {
			driftHosts++
		}
		if wb := r.GetWriteBack(); len(wb) > 0 {
			writeBacks++
			kernelEntity = wb[0]
		}
	}

	// Per-host fan-out — the thing opentofu never had.
	if perHost["web-1"] != pluginv1.ItemResult_STATUS_CHANGED ||
		perHost["web-3"] != pluginv1.ItemResult_STATUS_FAILED ||
		perHost["web-4"] != pluginv1.ItemResult_STATUS_UNREACHABLE {
		t.Fatalf("per-host ItemResult fan-out wrong: %+v", perHost)
	}
	// Facts write-back keyed by the target's identity (MF4).
	if writeBacks != 1 || kernelEntity.GetIdentityKeys()["host.name"] != "web-2" {
		t.Fatalf("facts write-back must be keyed by the target identity: %+v", kernelEntity)
	}
	var kernel map[string]string
	if err := json.Unmarshal(kernelEntity.GetFacets()["os.kernel"], &kernel); err != nil || kernel["family"] != "linux" {
		t.Fatalf("os.kernel facet wrong: %v (%s)", err, kernelEntity.GetFacets()["os.kernel"])
	}
	// Drift on the changed task (paths only — no bodies).
	if driftHosts != 1 {
		t.Fatalf("want one drift fragment (the changed task), got %d", driftHosts)
	}
	// MF5: the banner line surfaced as a diagnostic, never dropped.
	if diagnostics < 1 {
		t.Fatal("a non-event line must surface as a diagnostic TaskEvent (MF5)")
	}
	// Exactly one terminal; rc=1 → ok=false (the hub folds Succeeded, not the shim).
	if terminals != 1 || termOk {
		t.Fatalf("want one terminal with ok=false (rc=1), got terminals=%d ok=%v", terminals, termOk)
	}
}

// captureRunner records the args ansible-runner was invoked with.
type captureRunner struct {
	rc   int
	args []string
}

func (c *captureRunner) run(_ context.Context, _ string, args []string, _ func([]byte)) (int, error) {
	c.args = args
	return c.rc, nil
}

// TestShim_SCMContentRef proves an SCM content-ref is cloned in the EE and the play
// runs FROM the cloned playbook path (ADR-0025): the shim validates the ref, clones
// project/, and points ansible-runner at the repo's playbook — never a written
// play.yml. The clone boundary is a subprocess (git), injected here.
func TestShim_SCMContentRef(t *testing.T) {
	req := Request{
		Params:  json.RawMessage(`{"scm":{"repo":"https://git.example/ops.git","ref":"v2","playbook":"site/deploy.yml"}}`),
		Targets: []Target{{Name: "web-1"}},
		DryRun:  true,
	}
	var gotSCM scmParams
	var gotProjectDir string
	clone := func(_ context.Context, projectDir string, scm scmParams) error {
		gotSCM, gotProjectDir = scm, projectDir
		return nil // a real git clone would populate projectDir
	}
	run := &captureRunner{rc: 0}

	var buf bytes.Buffer
	dir := t.TempDir()
	if err := Run(context.Background(), &buf, dir, req, run, clone); err != nil {
		t.Fatalf("run: %v", err)
	}
	// The ref was cloned into project/ with the exact params.
	if gotSCM.Repo != "https://git.example/ops.git" || gotSCM.Ref != "v2" || gotSCM.Playbook != "site/deploy.yml" {
		t.Fatalf("cloner got wrong SCM params: %+v", gotSCM)
	}
	if gotProjectDir != dir+"/project" {
		t.Fatalf("clone target must be the runner project dir, got %q", gotProjectDir)
	}
	// ansible-runner runs the repo's playbook (not a written play.yml), in check mode.
	var sawPlaybook, sawCheck bool
	for i, a := range run.args {
		if a == "-p" && i+1 < len(run.args) && run.args[i+1] == "site/deploy.yml" {
			sawPlaybook = true
		}
		if a == "--check --diff" {
			sawCheck = true
		}
	}
	if !sawPlaybook {
		t.Fatalf("ansible-runner must target the SCM playbook path, args=%v", run.args)
	}
	if !sawCheck {
		t.Fatalf("DryRun must map to --check --diff, args=%v", run.args)
	}
}

// TestShim_SCMValidation proves the argument-injection / path-traversal guards on the
// SCM ref (a repo/ref beginning with '-' is a git option, not a URL; a playbook must
// stay within the repo). Each rejects with a terminal fatal, never a clone.
func TestShim_SCMValidation(t *testing.T) {
	cases := map[string]scmParams{
		"missing repo":       {Playbook: "p.yml"},
		"missing playbook":   {Repo: "r"},
		"repo option-inject": {Repo: "--upload-pack=evil", Playbook: "p.yml"},
		"ref option-inject":  {Repo: "r", Ref: "--foo", Playbook: "p.yml"},
		"playbook traversal": {Repo: "r", Playbook: "../../etc/x.yml"},
		"playbook absolute":  {Repo: "r", Playbook: "/etc/x.yml"},
	}
	for name, scm := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateSCM(&scm); err == nil {
				t.Fatalf("validateSCM(%+v) must reject", scm)
			}
			// End to end: a bad ref never reaches the cloner; it emits a terminal fatal.
			raw, _ := json.Marshal(map[string]any{"scm": scm})
			req := Request{Params: raw, Targets: []Target{{Name: "h"}}}
			clone := func(context.Context, string, scmParams) error {
				t.Fatal("a rejected SCM ref must never be cloned")
				return nil
			}
			var buf bytes.Buffer
			if err := Run(context.Background(), &buf, t.TempDir(), req, &captureRunner{}, clone); err != nil {
				t.Fatalf("run: %v", err)
			}
			if !bytes.Contains(buf.Bytes(), []byte(`"terminal":true`)) {
				t.Fatalf("a rejected SCM ref must emit a terminal fatal, got %s", buf.Bytes())
			}
		})
	}
}
