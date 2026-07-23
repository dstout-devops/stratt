package helm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// fakeHelm scripts helm invocations by their subcommand (args[0]: template/upgrade)
// — the plugin's content-expertise is exercised with NO helm binary (the ADR-0046
// module-isolation proof). It records the last args so tests can assert the exact
// Helm-4 flags the shim emits.
type fakeHelm struct {
	out      map[string]string
	rc       map[string]int
	lastArgs []string
}

func (f *fakeHelm) run(_ context.Context, _ string, _, args []string, onLine func([]byte)) ([]byte, int, error) {
	f.lastArgs = args
	out := f.out[args[0]]
	for _, l := range strings.Split(out, "\n") {
		if onLine != nil {
			onLine([]byte(l))
		}
	}
	return []byte(out), f.rc[args[0]], nil
}

type applyCapture struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*pluginv1.ApplyResponse
}

func (c *applyCapture) Send(m *pluginv1.ApplyResponse) error { c.msgs = append(c.msgs, m); return nil }
func (c *applyCapture) Context() context.Context             { return c.ctx }

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newServer(f *fakeHelm) *Server {
	s := NewServer(Config{PluginID: "helm"}, discard())
	s.run = f
	return s
}

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

func argsContain(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

const renderedWithSecret = `# Source: app/templates/cm.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app
data:
  greeting: hello
---
# Source: app/templates/secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: app
type: Opaque
data:
  password: c3VwZXJzZWNyZXQ=
stringData:
  token: plaintext-token
`

const desired = `{"chart":"app","release":"app","namespace":"default"}`

func TestGetManifest_ActuatorPlanApplyNoDestroy(t *testing.T) {
	m, _ := newServer(&fakeHelm{}).GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	man := m.GetManifest()
	if man.GetClass() != pluginv1.PluginClass_PLUGIN_CLASS_ACTUATOR {
		t.Fatalf("class must be ACTUATOR, got %v", man.GetClass())
	}
	verbs := map[pluginv1.Verb]bool{}
	for _, v := range man.GetVerbs() {
		verbs[v] = true
	}
	if !verbs[pluginv1.Verb_VERB_PLAN] || !verbs[pluginv1.Verb_VERB_APPLY] {
		t.Fatalf("actuator must advertise PLAN + APPLY, got %v", man.GetVerbs())
	}
	if !verbs[pluginv1.Verb_VERB_INVOKE] {
		t.Fatal("actuator must advertise INVOKE (the targetless helm/deploy Action — dual-surface)")
	}
	if verbs[pluginv1.Verb_VERB_DESTROY] {
		t.Fatal("v1 must NOT advertise DESTROY (ADR-0092 §4 — uninstall arrives with its own review)")
	}
}

type invokeCapture struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*pluginv1.InvokeResponse
}

func (c *invokeCapture) Send(m *pluginv1.InvokeResponse) error {
	c.msgs = append(c.msgs, m)
	return nil
}
func (c *invokeCapture) Context() context.Context { return c.ctx }

func runInvoke(t *testing.T, s *Server, action, args string, dryRun bool) []*pluginv1.InvokeResponse {
	t.Helper()
	cap := &invokeCapture{ctx: context.Background()}
	err := s.Invoke(&pluginv1.InvokeRequest{
		Action: action, Args: &pluginv1.Payload{Bytes: []byte(args)}, DryRun: dryRun,
		Envelope: &pluginv1.Envelope{CorrelationId: "cid-1"},
	}, cap)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	return cap.msgs
}

func invokeTerminal(msgs []*pluginv1.InvokeResponse) *pluginv1.InvokeResponse {
	for _, m := range msgs {
		if m.GetEvent().GetTerminal() {
			return m
		}
	}
	return nil
}

// TestInvoke_DeployAction proves the targetless helm/deploy Action runs
// `helm upgrade --install` (Helm-4 flags), streams with the correlation id, and
// folds an ok terminal naming its output contract.
func TestInvoke_DeployAction(t *testing.T) {
	f := &fakeHelm{out: map[string]string{"upgrade": "Release \"app\" has been deployed"}, rc: map[string]int{"upgrade": 0}}
	msgs := runInvoke(t, newServer(f), "helm/deploy", desired, false)
	if f.lastArgs[0] != "upgrade" || !argsContain(f.lastArgs, "--install") || !argsContain(f.lastArgs, "--rollback-on-failure") {
		t.Fatalf("Invoke must run helm upgrade --install with Helm-4 flags, got %v", f.lastArgs)
	}
	term := invokeTerminal(msgs)
	if term == nil || !term.GetEvent().GetOk() {
		t.Fatal("a successful deploy must terminate ok")
	}
	if term.GetEvent().GetCorrelationId() != "cid-1" {
		t.Fatal("the terminal must carry the correlation id (§1.8 descent)")
	}
	if term.GetResult().GetOutputContract().GetSchemaId() == "" {
		t.Fatal("the deploy action must name an output contract")
	}
	// The output Contract demands an OBJECT {release, namespace} — the Action must emit
	// the typed Outputs payload, not only name the contract, or ValidateActionOutputs
	// sees null and fails the Run (regression: the first functional self-deploy hit this).
	raw := term.GetResult().GetOutputs().GetBytes()
	if len(raw) == 0 {
		t.Fatal("the deploy action must emit the typed Outputs payload, not just the contract ref")
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Outputs must be a JSON object: %v", err)
	}
	if got["release"] != "app" || got["namespace"] != "default" {
		t.Fatalf("Outputs must carry the deployed release identity, got %v", got)
	}
}

// TestInvoke_UnknownActionRejected proves an unknown action name is refused.
func TestInvoke_UnknownActionRejected(t *testing.T) {
	err := newServer(&fakeHelm{}).Invoke(
		&pluginv1.InvokeRequest{Action: "helm/bogus", Args: &pluginv1.Payload{Bytes: []byte(desired)}},
		&invokeCapture{ctx: context.Background()})
	if err == nil {
		t.Fatal("an unknown action must be rejected")
	}
}

// TestInvoke_FailureFolds proves a non-zero helm upgrade folds to a not-ok terminal.
func TestInvoke_FailureFolds(t *testing.T) {
	f := &fakeHelm{out: map[string]string{"upgrade": "Error: UPGRADE FAILED"}, rc: map[string]int{"upgrade": 1}}
	term := invokeTerminal(runInvoke(t, newServer(f), "helm/deploy", desired, false))
	if term == nil || term.GetEvent().GetOk() {
		t.Fatal("a failed deploy must terminate not-ok")
	}
}

// TestPlan_RedactsSecretAndPinsDigest proves helm template renders a review artifact
// with Secret data masked (§2.5) and a sha256 of the redacted bytes the Gate pins.
func TestPlan_RedactsSecretAndPinsDigest(t *testing.T) {
	f := &fakeHelm{out: map[string]string{"template": renderedWithSecret}, rc: map[string]int{"template": 0}}
	resp, err := newServer(f).Plan(context.Background(), &pluginv1.PlanRequest{Desired: &pluginv1.Payload{Bytes: []byte(desired)}})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if f.lastArgs[0] != "template" {
		t.Fatalf("Plan must run `helm template`, got %v", f.lastArgs)
	}
	diff := string(resp.GetDiff().GetBytes())
	if strings.Contains(diff, "c3VwZXJzZWNyZXQ=") || strings.Contains(diff, "plaintext-token") {
		t.Fatalf("Secret data/stringData must be redacted from the plan diff:\n%s", diff)
	}
	if !strings.Contains(diff, "(redacted)") {
		t.Fatalf("expected a (redacted) marker in:\n%s", diff)
	}
	if !strings.Contains(diff, "greeting: hello") {
		t.Fatalf("non-Secret manifest data must survive redaction:\n%s", diff)
	}
	sum := sha256.Sum256(resp.GetDiff().GetBytes())
	if resp.GetPlan().GetSha256() != hex.EncodeToString(sum[:]) {
		t.Fatal("plan digest must be sha256 of the redacted manifests the Gate reviews")
	}
	if resp.GetPlan().GetMediaType() == "" {
		t.Fatal("plan artifact needs a media type")
	}
	// SavedPlan (shipped to the core store) must also be redacted — no Secret crosses.
	if strings.Contains(string(resp.GetSavedPlan()), "c3VwZXJzZWNyZXQ=") {
		t.Fatal("SavedPlan must be redacted before it reaches the core store (§2.5)")
	}
}

// TestApply_SuccessChanged proves a real apply streams helm output as TaskEvents and
// folds a release-root CHANGED ItemResult.
func TestApply_SuccessChanged(t *testing.T) {
	f := &fakeHelm{
		out: map[string]string{"upgrade": "Release \"app\" has been upgraded. Happy Helming!\nNAME: app\nSTATUS: deployed"},
		rc:  map[string]int{"upgrade": 0},
	}
	msgs := runApply(t, newServer(f), desired, false)
	if f.lastArgs[0] != "upgrade" || !argsContain(f.lastArgs, "--install") {
		t.Fatalf("Apply must run `helm upgrade --install`, got %v", f.lastArgs)
	}
	var sawLog bool
	for _, m := range msgs {
		if m.GetEvent().GetFields()["kind"] == "helm" {
			sawLog = true
		}
	}
	if !sawLog {
		t.Fatal("helm output must stream as TaskEvents (§1.8)")
	}
	term := terminal(msgs)
	if term == nil || !term.GetEvent().GetOk() {
		t.Fatal("a successful apply must terminate ok")
	}
	if term.GetResult().GetItemKey() != "" || term.GetResult().GetStatus() != pluginv1.ItemResult_STATUS_CHANGED {
		t.Fatalf("release-root ItemResult must be CHANGED with empty item_key, got %+v", term.GetResult())
	}
}

// TestApply_UsesHelm4Flags proves the shim emits Helm-4 spellings, not the removed
// v3 --atomic (dependency-scout binding condition).
func TestApply_UsesHelm4Flags(t *testing.T) {
	f := &fakeHelm{out: map[string]string{"upgrade": "STATUS: deployed"}, rc: map[string]int{"upgrade": 0}}
	runApply(t, newServer(f), `{"chart":"app","release":"app","namespace":"default","createNamespace":true}`, false)
	if !argsContain(f.lastArgs, "--rollback-on-failure") {
		t.Fatalf("apply must use Helm-4 --rollback-on-failure, got %v", f.lastArgs)
	}
	if argsContain(f.lastArgs, "--atomic") {
		t.Fatal("must not use the removed Helm-3 --atomic flag")
	}
	if !argsContain(f.lastArgs, "--wait") || !argsContain(f.lastArgs, "--create-namespace") {
		t.Fatalf("apply must --wait and honor createNamespace, got %v", f.lastArgs)
	}
}

// TestApply_DryRunServerSideOk proves a dry run uses --dry-run=server, does not
// mutate (OK, not CHANGED), and never rolls back / waits.
func TestApply_DryRunServerSideOk(t *testing.T) {
	f := &fakeHelm{out: map[string]string{"upgrade": "NAME: app\nSTATUS: pending-install"}, rc: map[string]int{"upgrade": 0}}
	msgs := runApply(t, newServer(f), desired, true)
	if !argsContain(f.lastArgs, "--dry-run=server") {
		t.Fatalf("dry run must use --dry-run=server, got %v", f.lastArgs)
	}
	if argsContain(f.lastArgs, "--rollback-on-failure") || argsContain(f.lastArgs, "--wait") {
		t.Fatalf("dry run must not roll back or wait, got %v", f.lastArgs)
	}
	term := terminal(msgs)
	if term.GetResult().GetStatus() != pluginv1.ItemResult_STATUS_OK || !term.GetEvent().GetOk() {
		t.Fatalf("a successful dry run is OK (nothing mutated), got %+v", term.GetResult())
	}
}

// TestApply_FailureFolds proves a non-zero helm apply folds to FAILED, and an
// "Error:" line lifts to an ERROR diagnostic (§1.8 — nothing swallowed).
func TestApply_FailureFolds(t *testing.T) {
	f := &fakeHelm{
		out: map[string]string{"upgrade": "Error: UPGRADE FAILED: timed out waiting for the condition"},
		rc:  map[string]int{"upgrade": 1},
	}
	msgs := runApply(t, newServer(f), desired, false)
	var sawErr bool
	for _, m := range msgs {
		if m.GetEvent().GetLevel() == pluginv1.TaskEvent_LEVEL_ERROR && m.GetEvent().GetFields()["kind"] == "diagnostic" {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("an Error: line must lift to an ERROR diagnostic event")
	}
	term := terminal(msgs)
	if term == nil || term.GetEvent().GetOk() {
		t.Fatal("a failed apply must terminate not-ok")
	}
	if term.GetResult().GetStatus() != pluginv1.ItemResult_STATUS_FAILED {
		t.Fatalf("failed apply must fold to FAILED, got %v", term.GetResult().GetStatus())
	}
}

// TestApply_InvalidParamsFailsVisibly proves missing required params fail the Apply
// terminally rather than silently.
func TestApply_InvalidParamsFailsVisibly(t *testing.T) {
	term := terminal(runApply(t, newServer(&fakeHelm{}), `{"release":"app","namespace":"default"}`, false))
	if term == nil || term.GetResult().GetStatus() != pluginv1.ItemResult_STATUS_FAILED {
		t.Fatalf("missing chart must fail the Apply visibly, got %+v", term.GetResult())
	}
}
