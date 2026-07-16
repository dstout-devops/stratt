package opentofu

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// fakeTofu scripts tofu invocations by their first arg (init/apply/plan/output/
// show) — the plugin's content-expertise is exercised with NO tofu binary (the
// ADR-0046 module-isolation proof). lines stream via onLine; full is returned for
// capture commands (output/show); rc is the exit code.
type fakeTofu struct {
	lines map[string][]string
	full  map[string]string
	rc    map[string]int
}

func (f *fakeTofu) run(_ context.Context, _ string, _, args []string, onLine func([]byte)) ([]byte, int, error) {
	cmd := args[0]
	for _, l := range f.lines[cmd] {
		if onLine != nil {
			onLine([]byte(l))
		}
	}
	full := f.full[cmd]
	if full == "" {
		full = strings.Join(f.lines[cmd], "\n")
	}
	return []byte(full), f.rc[cmd], nil
}

// applyCapture is a minimal ServerStreamingServer collecting sent responses.
type applyCapture struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*pluginv1.ApplyResponse
}

func (c *applyCapture) Send(m *pluginv1.ApplyResponse) error { c.msgs = append(c.msgs, m); return nil }
func (c *applyCapture) Context() context.Context             { return c.ctx }

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newServer(f *fakeTofu) *Server {
	s := NewServer(Config{PluginID: "opentofu"}, discard())
	s.run = f
	return s
}

const strattEntitiesOutputs = `{
  "instance_id": {"sensitive": false, "type": "string", "value": "i-123"},
  "stratt_entities": {"sensitive": false,
    "type": ["list", ["object", {"kind": "string", "identityKeys": ["map", "string"]}]],
    "value": [{"kind": "vm", "identityKeys": {"aws.instanceId": "i-123"}, "labels": {"role": "web"}}]}
}`

func runApply(t *testing.T, s *Server, desired string, dryRun bool) []*pluginv1.ApplyResponse {
	t.Helper()
	cap := &applyCapture{ctx: context.Background()}
	if err := s.Apply(&pluginv1.ApplyRequest{Desired: &pluginv1.Payload{Bytes: []byte(desired)}, DryRun: dryRun}, cap); err != nil {
		t.Fatalf("apply: %v", err)
	}
	return cap.msgs
}

func terminal(msgs []*pluginv1.ApplyResponse) *pluginv1.ApplyResponse {
	for _, m := range msgs {
		if m.GetEvent().GetTerminal() {
			return m
		}
	}
	return nil
}

// TestApply_SuccessLiftsOutputsAndFolds proves the proven Apply path over a real
// tool: streamed TaskEvents, stratt_entities → governed write-back, a rung-2
// DerivedContract, and a terminal workspace-ROOT ItemResult (item_key "").
func TestApply_SuccessLiftsOutputsAndFolds(t *testing.T) {
	f := &fakeTofu{
		lines: map[string][]string{
			"init":  {`{"@level":"info","@message":"Initializing"}`},
			"apply": {`{"@level":"info","@message":"aws_instance.web: Creating","type":"apply_start"}`, `{"@level":"info","@message":"Apply complete","type":"change_summary","changes":{"add":1,"change":0,"remove":0,"operation":"apply"}}`},
		},
		full: map[string]string{"output": strattEntitiesOutputs},
		rc:   map[string]int{"init": 0, "apply": 0, "output": 0},
	}
	msgs := runApply(t, newServer(f), `{"module":"web","workspace":"prod"}`, false)

	var gotWriteBack *pluginv1.ObservedEntity
	var gotDerived *pluginv1.DerivedContract
	var sawApplyStart bool
	for _, m := range msgs {
		if m.GetEvent().GetFields()["type"] == "apply_start" {
			sawApplyStart = true
		}
		if len(m.GetWriteBack()) > 0 {
			gotWriteBack = m.GetWriteBack()[0]
		}
		if m.GetDerivedContract() != nil {
			gotDerived = m.GetDerivedContract()
		}
	}
	if !sawApplyStart {
		t.Fatal("tofu -json lines must stream as TaskEvents for §1.8 descent")
	}
	if gotWriteBack == nil || gotWriteBack.GetKind() != "vm" || gotWriteBack.GetIdentityKeys()["aws.instanceId"] != "i-123" {
		t.Fatalf("stratt_entities must become governed write-back: %+v", gotWriteBack)
	}
	if gotWriteBack.GetLabels()["role"] != "web" {
		t.Fatalf("write-back labels lost: %+v", gotWriteBack.GetLabels())
	}
	if gotDerived == nil || gotDerived.GetRung() != pluginv1.DerivedContract_RUNG_TOOL_DERIVED ||
		gotDerived.GetSchemaId() != "opentofu/prod.outputs" {
		t.Fatalf("outputs must derive a rung-2 DerivedContract: %+v", gotDerived)
	}
	// The derived schema must be valid JSON describing the outputs.
	var schema map[string]any
	if err := json.Unmarshal(gotDerived.GetSchema(), &schema); err != nil {
		t.Fatalf("derived schema is not valid JSON: %v", err)
	}
	term := terminal(msgs)
	if term == nil || !term.GetEvent().GetOk() {
		t.Fatal("a successful apply must terminate ok")
	}
	if term.GetResult().GetItemKey() != "" || term.GetResult().GetStatus() != pluginv1.ItemResult_STATUS_CHANGED {
		t.Fatalf("workspace-root ItemResult must be CHANGED with empty item_key, got %+v", term.GetResult())
	}
}

// TestApply_FailureFoldsNotOk proves a non-zero tofu apply folds to a terminal
// FAILED status (the host then computes Succeeded=false core-side).
func TestApply_FailureFoldsNotOk(t *testing.T) {
	f := &fakeTofu{
		lines: map[string][]string{
			"init":  {`{"@level":"info","@message":"Initializing"}`},
			"apply": {`{"@level":"error","@message":"boom","diagnostic":{"severity":"error","summary":"resource failed"}}`},
		},
		rc: map[string]int{"init": 0, "apply": 1},
	}
	term := terminal(runApply(t, newServer(f), `{"module":"web","workspace":"prod"}`, false))
	if term == nil || term.GetEvent().GetOk() {
		t.Fatal("a failed apply must terminate not-ok")
	}
	if term.GetResult().GetStatus() != pluginv1.ItemResult_STATUS_FAILED {
		t.Fatalf("failed apply must fold to STATUS_FAILED, got %v", term.GetResult().GetStatus())
	}
}

// TestApply_DryRunPlanEscalatesToChangedWithDrift proves a streaming dry-run plan
// with changes escalates to CHANGED and emits a redacted drift fragment (ADR-0019)
// — and runs NO output/write-back (a plan is diagnostic, not the pin path).
func TestApply_DryRunPlanEscalatesToChangedWithDrift(t *testing.T) {
	f := &fakeTofu{
		lines: map[string][]string{
			"init": {`{"@level":"info","@message":"Initializing"}`},
			"plan": {`{"@level":"info","@message":"Plan: 2 to add","type":"change_summary","changes":{"add":2,"change":0,"remove":0,"operation":"plan"}}`},
		},
		rc: map[string]int{"init": 0, "plan": 0},
	}
	msgs := runApply(t, newServer(f), `{"module":"web","workspace":"prod"}`, true)
	var sawDrift bool
	for _, m := range msgs {
		if m.GetDrift() != nil {
			sawDrift = true
			var d map[string]any
			if err := json.Unmarshal(m.GetDrift().GetDetail().GetBytes(), &d); err != nil || d["add"] == nil {
				t.Fatalf("drift fragment malformed: %s", m.GetDrift().GetDetail().GetBytes())
			}
		}
		if len(m.GetWriteBack()) > 0 {
			t.Fatal("a dry-run plan must NOT write back (diagnostic only)")
		}
	}
	if !sawDrift {
		t.Fatal("a plan that would change must emit a drift fragment (ADR-0019)")
	}
	term := terminal(msgs)
	if term.GetResult().GetStatus() != pluginv1.ItemResult_STATUS_CHANGED || !term.GetEvent().GetOk() {
		t.Fatalf("a dry-run plan with changes is CHANGED + ok, got %+v", term.GetResult())
	}
}

// TestApply_ReservedLabelPrefixFailsVisibly proves a stratt.* label in
// stratt_entities fails the Apply (never silently overwritten, §2.4 spirit).
func TestApply_ReservedLabelPrefixFailsVisibly(t *testing.T) {
	bad := `{"stratt_entities": {"sensitive": false, "type": ["list",["object",{"kind":"string"}]],
	  "value": [{"kind":"vm","identityKeys":{"aws.instanceId":"i-9"},"labels":{"stratt.workspace":"evil"}}]}}`
	f := &fakeTofu{
		lines: map[string][]string{"init": {`{"@message":"init"}`}, "apply": {`{"@message":"done","type":"apply_complete"}`}},
		full:  map[string]string{"output": bad},
		rc:    map[string]int{"init": 0, "apply": 0, "output": 0},
	}
	term := terminal(runApply(t, newServer(f), `{"module":"web","workspace":"prod"}`, false))
	if term.GetResult().GetStatus() != pluginv1.ItemResult_STATUS_FAILED {
		t.Fatalf("a reserved stratt.* label must fail the Apply visibly, got %v", term.GetResult().GetStatus())
	}
}

func TestGetManifest_Actuator(t *testing.T) {
	m, _ := newServer(&fakeTofu{}).GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	man := m.GetManifest()
	if man.GetClass() != pluginv1.PluginClass_PLUGIN_CLASS_ACTUATOR {
		t.Fatalf("class must be ACTUATOR, got %v", man.GetClass())
	}
	verbs := map[pluginv1.Verb]bool{}
	for _, v := range man.GetVerbs() {
		verbs[v] = true
	}
	if !verbs[pluginv1.Verb_VERB_PLAN] || !verbs[pluginv1.Verb_VERB_APPLY] || !verbs[pluginv1.Verb_VERB_DESTROY] {
		t.Fatalf("actuator must advertise PLAN/APPLY/DESTROY, got %v", man.GetVerbs())
	}
}
