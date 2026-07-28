package opentofu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// invokeCapture collects the Invoke stream (the Action twin of applyCapture).
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

// terminal returns the terminal event of a captured Invoke stream.
func (c *invokeCapture) terminal(t *testing.T) *pluginv1.TaskEvent {
	t.Helper()
	for _, m := range c.msgs {
		if ev := m.GetEvent(); ev != nil && ev.GetTerminal() {
			return ev
		}
	}
	t.Fatalf("no terminal event in %d messages", len(c.msgs))
	return nil
}

func (c *invokeCapture) result() *pluginv1.InvokeResult {
	for _, m := range c.msgs {
		if r := m.GetResult(); r != nil {
			return r
		}
	}
	return nil
}

func runInvoke(t *testing.T, s *Server, args string, dryRun bool, caps map[string]*pluginv1.CapabilityHandle) *invokeCapture {
	t.Helper()
	cap := &invokeCapture{ctx: context.Background()}
	req := &pluginv1.InvokeRequest{
		Action:               actionApply,
		Args:                 &pluginv1.Payload{Bytes: []byte(args)},
		DryRun:               dryRun,
		ResolvedCapabilities: caps,
	}
	if err := s.Invoke(req, cap); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	return cap
}

// The aws-network module's real reserved output, shaped as `tofu output -json` returns it.
const subnetOutputs = `{
  "subnet_id": {"sensitive": false, "type": "string", "value": "subnet-0abc"},
  "vpc_id": {"sensitive": false, "type": "string", "value": "vpc-0def"},
  "admin_password": {"sensitive": true, "type": "string", "value": "hunter2"},
  "stratt_entities": {"sensitive": false,
    "type": ["list", ["object", {"kind": "string", "identityKeys": ["map", "string"]}]],
    "value": [{"kind": "subnet", "identityKeys": {"aws.subnetId": "subnet-0abc"},
               "labels": {"source": "opentofu", "net.cidr": "10.30.0.0/24"}}]}
}`

const buildArgs = `{"module":"aws-network","workspace":"app-subnet",
  "projectKind":"subnet",
  "projectLabels":{"stratt.intent/singleton":"Intent/Subnet/app-subnet","tier":"app"}}`

func okTofu() *fakeTofu {
	return &fakeTofu{
		lines: map[string][]string{},
		full:  map[string]string{"output": subnetOutputs},
		rc:    map[string]int{},
	}
}

// The overlay is the whole reason this Action exists rather than an actuation: the
// correlation label CANNOT come out of the module (the reserved stratt.* prefix is
// refused there), so it must arrive from the launch and land on the projection. A build
// whose Entity lacks stratt.intent/singleton never resolves the Finding it was launched
// from — and the Run still goes green, which is why this is asserted and not assumed.
func TestApplyActionProjectsWithTheEstateOverlay(t *testing.T) {
	cap := runInvoke(t, newServer(okTofu()), buildArgs, false, nil)
	if ev := cap.terminal(t); !ev.GetOk() {
		t.Fatalf("terminal not ok: %s", ev.GetMessage())
	}
	res := cap.result()
	if res == nil || len(res.GetEntities()) != 1 {
		t.Fatalf("want exactly one projected entity, got %+v", res)
	}
	ent := res.GetEntities()[0]
	if ent.GetKind() != "subnet" {
		t.Errorf("kind = %q, want subnet", ent.GetKind())
	}
	if got := ent.GetIdentityKeys()["aws.subnetId"]; got != "subnet-0abc" {
		t.Errorf("identity aws.subnetId = %q, want subnet-0abc", got)
	}
	if got := ent.GetLabels()["stratt.intent/singleton"]; got != "Intent/Subnet/app-subnet" {
		t.Errorf("correlation label = %q, want Intent/Subnet/app-subnet — without it the "+
			"provision Finding never resolves and the build is offered again forever", got)
	}
	// The module's own descriptive labels survive; the overlay adds, it does not replace.
	if got := ent.GetLabels()["source"]; got != "opentofu" {
		t.Errorf("module label source = %q, want opentofu (the overlay must not drop it)", got)
	}
	if got := ent.GetLabels()["tier"]; got != "app" {
		t.Errorf("overlay label tier = %q, want app", got)
	}
}

// §2.5: a Run's captured outputs are not a secret channel, and the content-blind core
// cannot tell which of a module's outputs tofu marked sensitive — only the plugin can.
func TestApplyActionOutputsRedactSensitiveAndDropTheProjectionChannel(t *testing.T) {
	cap := runInvoke(t, newServer(okTofu()), buildArgs, false, nil)
	var out struct {
		Module    string         `json:"module"`
		Workspace string         `json:"workspace"`
		Outputs   map[string]any `json:"outputs"`
	}
	if err := json.Unmarshal(cap.result().GetOutputs().GetBytes(), &out); err != nil {
		t.Fatalf("decode outputs: %v", err)
	}
	if out.Module != "aws-network" || out.Workspace != "app-subnet" {
		t.Errorf("module/workspace = %q/%q", out.Module, out.Workspace)
	}
	if out.Outputs["subnet_id"] != "subnet-0abc" {
		t.Errorf("subnet_id = %v, want subnet-0abc", out.Outputs["subnet_id"])
	}
	if got := out.Outputs["admin_password"]; got != "(sensitive)" {
		t.Errorf("sensitive output leaked as %v", got)
	}
	if _, present := out.Outputs["stratt_entities"]; present {
		t.Error("stratt_entities is the governed projection channel and must not also ride as an output value")
	}
}

// §2.4: two answers to "what kind is this Entity" is refused, never resolved by a rule.
// Silently letting either win lands a real, wrongly-typed Entity — and the graph's
// correlate branch RETYPES on upsert, so a wrong kind spreads instead of sitting still.
func TestApplyActionRefusesAKindItCannotReconcile(t *testing.T) {
	args := `{"module":"aws-network","workspace":"app-subnet","projectKind":"vlan"}`
	ev := runInvoke(t, newServer(okTofu()), args, false, nil).terminal(t)
	if ev.GetOk() {
		t.Fatal("a module kind disagreeing with the estate's projectKind must fail the build")
	}
	if !strings.Contains(ev.GetMessage(), "§2.4") {
		t.Errorf("message should name the rule it is applying, got %q", ev.GetMessage())
	}
}

func TestApplyActionRefusesAnOverlayLabelConflict(t *testing.T) {
	args := `{"module":"aws-network","workspace":"app-subnet","projectKind":"subnet",
	          "projectLabels":{"source":"crossplane"}}`
	ev := runInvoke(t, newServer(okTofu()), args, false, nil).terminal(t)
	if ev.GetOk() {
		t.Fatal("an overlay label disagreeing with the module's must fail, not silently win")
	}
}

// A build with no kind would land an Entity no View selects and no reconcile closes.
func TestApplyActionRefusesAMissingProjectKind(t *testing.T) {
	args := `{"module":"aws-network","workspace":"app-subnet"}`
	ev := runInvoke(t, newServer(okTofu()), args, false, nil).terminal(t)
	if ev.GetOk() {
		t.Fatal("projectKind is required")
	}
}

// The apply succeeded and the graph learned nothing: infrastructure exists that nothing
// has projected, and the next reconcile offers to build it again. Silence is the failure
// mode here, so it is asserted to be loud.
func TestApplyActionFailsWhenTheBuildProjectsNothing(t *testing.T) {
	f := okTofu()
	f.full["output"] = `{"subnet_id": {"sensitive": false, "type": "string", "value": "subnet-0abc"}}`
	ev := runInvoke(t, newServer(f), buildArgs, false, nil).terminal(t)
	if ev.GetOk() {
		t.Fatal("a module with no stratt_entities must fail the build, not report success")
	}
	if !strings.Contains(ev.GetMessage(), "stratt_entities") {
		t.Errorf("message should name the missing output, got %q", ev.GetMessage())
	}
}

func TestApplyActionFailsWhenReadingOutputsBackFails(t *testing.T) {
	f := okTofu()
	f.rc["output"] = 1
	ev := runInvoke(t, newServer(f), buildArgs, false, nil).terminal(t)
	if ev.GetOk() {
		t.Fatal("apply-succeeded-but-unread must fail: the infrastructure exists and nothing projected it")
	}
}

func TestApplyActionFailsWhenTofuApplyFails(t *testing.T) {
	f := okTofu()
	f.rc["apply"] = 1
	ev := runInvoke(t, newServer(f), buildArgs, false, nil).terminal(t)
	if ev.GetOk() {
		t.Fatal("a non-zero tofu apply must be terminal-not-ok")
	}
	if cap := runInvoke(t, newServer(f), buildArgs, false, nil); cap.result() != nil && len(cap.result().GetEntities()) > 0 {
		t.Error("a failed apply must project nothing")
	}
}

// A dry-run says what WOULD exist. Projecting from it would close a build Finding with
// no infrastructure behind it (§1.2: the graph holds what is, not what is planned).
func TestApplyActionDryRunPlansAndProjectsNothing(t *testing.T) {
	f := okTofu()
	cap := runInvoke(t, newServer(f), buildArgs, true, nil)
	if ev := cap.terminal(t); !ev.GetOk() {
		t.Fatalf("dry-run should succeed: %s", ev.GetMessage())
	}
	if res := cap.result(); res != nil && len(res.GetEntities()) > 0 {
		t.Error("a dry-run must project no Entities")
	}
}

// ADR-0145 D2: the handles the declaration `requires` reach THIS verb. The ipam CIDR is
// injected as a module var by the shared prepare(), so a build gets its allocated network
// on the Action seam exactly as the Apply verb does. Before the port carried them, this
// ran with an unset var.stratt_ipam_cidr and nobody was told.
func TestApplyActionConsumesTheInjectedCapabilityHandles(t *testing.T) {
	s := newServer(okTofu())
	caps := map[string]*pluginv1.CapabilityHandle{
		"statestore": {Kind: "s3", Config: map[string]string{"bucket": "stratt-state"}},
		"ipam":       {Output: []byte(`{"cidr":"10.30.7.0/24","vlanId":42}`)},
	}
	// prepare() is the shared decode path both verbs use; assert the merge it performs.
	p, _, _, varFile, err := s.prepare([]byte(buildArgs), caps["statestore"], caps["ipam"])
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if varFile == "" {
		t.Fatal("an injected ipam handle must produce a -var-file")
	}
	if p.Vars["stratt_ipam_cidr"] != "10.30.7.0/24" {
		t.Errorf("stratt_ipam_cidr = %v, want the resolved handle's CIDR", p.Vars["stratt_ipam_cidr"])
	}
	// And the backend config reaches init the same way it does for Apply.
	args := s.initArgs(p.Workspace, caps["statestore"])
	if !strings.Contains(strings.Join(args, " "), "-backend-config=bucket=stratt-state") {
		t.Errorf("init args %v carry no injected backend config", args)
	}
	_ = runInvoke(t, s, buildArgs, false, caps) // and the whole path runs with them attached
}

func TestInvokeRefusesAnUnknownAction(t *testing.T) {
	cap := &invokeCapture{ctx: context.Background()}
	err := newServer(okTofu()).Invoke(&pluginv1.InvokeRequest{
		Action: "opentofu/destroy-everything",
		Args:   &pluginv1.Payload{Bytes: []byte(buildArgs)},
	}, cap)
	if err == nil {
		t.Fatal("an unadvertised action must be refused")
	}
}

// The Manifest must advertise exactly what the plugin serves — an Action registered from
// a declaration but absent from the Manifest fails provider verification at admission,
// and one advertised but unserved fails at the gate in front of an operator.
func TestManifestAdvertisesTheBuildAction(t *testing.T) {
	m, err := newServer(okTofu()).GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	var invoke bool
	for _, v := range m.GetManifest().GetVerbs() {
		if v == pluginv1.Verb_VERB_INVOKE {
			invoke = true
		}
	}
	if !invoke {
		t.Error("serving an Action without advertising VERB_INVOKE makes it unreachable")
	}
	decls := m.GetManifest().GetActions()
	if len(decls) != 1 || decls[0].GetName() != actionApply {
		t.Fatalf("actions = %+v, want exactly %s", decls, actionApply)
	}
	// Invariant #5: every ContractRef carries a real pinned digest, not a bare id.
	for _, ref := range []*pluginv1.ContractRef{decls[0].GetInput(), decls[0].GetOutput()} {
		if ref.GetSchemaId() == "" || ref.GetSha256() == "" {
			t.Errorf("contract ref %+v is unpinned", ref)
		}
	}
}
