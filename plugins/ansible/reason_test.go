package ansible

import (
	"strings"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// These are the regression for a diagnosis gap the app-cert demo walked into on its
// first live run. The play could not SSH to its target, and the Run's event stream
// said `runner_on_unreachable` — at INFO, with no reason. The sentence that actually
// explained it ("User appops not allowed because account is locked") was sitting in
// the event's own result payload, and finding it meant reading sshd's log on the
// target instead. An execution pod's logs are deleted with its Job, so for anyone
// without shell on the managed host there was nothing at all (§1.8).

// TestEventSeverity_FailuresAreNotInfo: ansible's own failure events must not reach
// the operator tinted like a task that worked. Level is the one severity signal every
// consumer reads (ADR-0117 g), so a wrong level is worse than no level.
func TestEventSeverity_FailuresAreNotInfo(t *testing.T) {
	errors := []string{"runner_on_failed", "runner_item_on_failed", "runner_on_unreachable", "runner_on_async_failed"}
	for _, name := range errors {
		if got := eventSeverity(RunnerEvent{Event: name}); got != pluginv1.TaskEvent_LEVEL_ERROR {
			t.Errorf("%s must be ERROR, got %v", name, got)
		}
	}
	for _, name := range []string{"runner_on_skipped", "runner_on_retry"} {
		if got := eventSeverity(RunnerEvent{Event: name}); got != pluginv1.TaskEvent_LEVEL_WARN {
			t.Errorf("%s must be WARN, got %v", name, got)
		}
	}
	for _, name := range []string{"runner_on_ok", "playbook_on_task_start", "runner_on_start"} {
		if got := eventSeverity(RunnerEvent{Event: name}); got != pluginv1.TaskEvent_LEVEL_INFO {
			t.Errorf("%s must stay INFO, got %v", name, got)
		}
	}
}

// TestEventReason_CarriesTheCause covers each place ansible puts the explanation.
func TestEventReason_CarriesTheCause(t *testing.T) {
	cases := []struct {
		name string
		ev   RunnerEvent
		want string
	}{
		{
			name: "the real unreachable case that motivated this",
			ev: RunnerEvent{Event: "runner_on_unreachable", EventData: map[string]any{
				"host": "app-node-1.stratt.test",
				"res": map[string]any{
					"unreachable": true,
					"msg":         "Failed to connect to the host via ssh: Connection closed by 10.244.0.5 port 22",
				},
			}},
			want: "Failed to connect to the host via ssh",
		},
		{
			name: "a module failure's own message",
			ev: RunnerEvent{Event: "runner_on_failed", EventData: map[string]any{
				"res": map[string]any{"msg": "Cannot import module 'cryptography'"},
			}},
			want: "Cannot import module 'cryptography'",
		},
		{
			name: "a failing command speaks through stderr",
			ev: RunnerEvent{Event: "runner_on_failed", EventData: map[string]any{
				"res": map[string]any{"msg": "", "stderr": "nginx: [emerg] cannot load certificate"},
			}},
			want: "nginx: [emerg] cannot load certificate",
		},
		{
			name: "some events carry the text at the top level, not under res",
			ev:   RunnerEvent{Event: "runner_on_unreachable", EventData: map[string]any{"msg": "ssh timed out"}},
			want: "ssh timed out",
		},
		{
			name: "no reason available is not an error, just empty",
			ev:   RunnerEvent{Event: "runner_on_ok", EventData: map[string]any{"res": map[string]any{"changed": true}}},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := eventReason(tc.ev)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("expected no reason, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("reason = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// TestEventReason_TruncatesVisibly: a module result can hold a whole command's
// stdout, so the reason is capped — but never silently (§1.8).
func TestEventReason_TruncatesVisibly(t *testing.T) {
	huge := strings.Repeat("x", reasonCap*3)
	got := eventReason(RunnerEvent{Event: "runner_on_failed", EventData: map[string]any{
		"res": map[string]any{"msg": huge},
	}})
	if len(got) <= reasonCap {
		t.Fatalf("expected the cap to keep %d bytes, got %d", reasonCap, len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("a truncated reason must say so, or an operator reads a sentence that stops mid-thought")
	}
}

// TestShim_UnreachableHostReportsWhy is the end-to-end shape: an unreachable host
// must reach the descent at ERROR, naming the cause, and the terminal must not be
// green. This is the exact event sequence the demo's first live run produced.
func TestShim_UnreachableHostReportsWhy(t *testing.T) {
	lines := []string{
		`{"event":"playbook_on_start","event_data":{}}`,
		`{"event":"runner_on_unreachable","event_data":{"host":"app-node-1.stratt.test",` +
			`"res":{"unreachable":true,"msg":"Failed to connect to the host via ssh: User appops not allowed because account is locked"}}}`,
		`{"event":"playbook_on_stats","event_data":{}}`,
	}
	req := Request{
		Params:  withDeclaredSSH(t, `{"play":"- hosts: all"}`),
		Targets: []Target{{Name: "app-node-1.stratt.test", Address: "app-node.stratt.svc.cluster.local"}},
	}
	got := runShim(t, req, fakeRunner{rc: 4, lines: lines}) // rc=4 is ansible's unreachable

	var sawReason bool
	for _, resp := range got {
		ev := resp.GetEvent()
		if ev == nil || ev.GetFields()["kind"] != "runner_on_unreachable" {
			continue
		}
		if ev.GetLevel() != pluginv1.TaskEvent_LEVEL_ERROR {
			t.Errorf("an unreachable host must be ERROR on the stream, got %v", ev.GetLevel())
		}
		if !strings.Contains(ev.GetMessage(), "account is locked") {
			t.Errorf("the event must carry ansible's own reason, got %q", ev.GetMessage())
		}
		sawReason = true
	}
	if !sawReason {
		t.Fatal("no runner_on_unreachable event reached the stream at all")
	}
}
