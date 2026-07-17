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

func runShim(t *testing.T, req Request, f fakeRunner) []*pluginv1.ApplyResponse {
	t.Helper()
	var buf bytes.Buffer
	if err := Run(context.Background(), &buf, t.TempDir(), req, f); err != nil {
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
