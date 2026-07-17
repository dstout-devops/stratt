package awsec2

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// fakeEC2 is the injected EC2 API — it lets us exercise the plugin's
// content-expertise in isolation, no AWS calls (the ADR-0046 module-isolation
// point: the plugin is its own test unit, importing neither core nor Postgres).
type fakeEC2 struct {
	describeOut *ec2.DescribeInstancesOutput
	describeErr error
	runOut      *ec2.RunInstancesOutput
	runErr      error
	lastRun     *ec2.RunInstancesInput
}

func (f *fakeEC2) DescribeInstances(_ context.Context, _ *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return f.describeOut, f.describeErr
}

func (f *fakeEC2) RunInstances(_ context.Context, in *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	f.lastRun = in
	return f.runOut, f.runErr
}

// captureStream is a fake grpc.ServerStreamingServer[T] recording sent messages.
// It mirrors how the vcenter tests exercise the server through the port surface,
// without a live gRPC connection.
type captureStream[T any] struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*T
}

func (s *captureStream[T]) Context() context.Context { return s.ctx }
func (s *captureStream[T]) Send(m *T) error          { s.sent = append(s.sent, m); return nil }

func newServer(t *testing.T, api EC2API) *Server {
	t.Helper()
	s := NewServer(Config{Region: "us-east-1"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.newAPI = func(context.Context) (EC2API, error) { return api, nil }
	return s
}

// TestObserveEmitsInstances proves the Syncer half of the port: DescribeInstances
// pages → instance ObservedEntities with the identity, labels, and facet blobs the
// wire carries, and the full_sync_complete boundary. Terminated instances are
// skipped (they count as absent → the host tombstones).
func TestObserveEmitsInstances(t *testing.T) {
	launch := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	api := &fakeEC2{describeOut: &ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{
			{
				InstanceId:       aws.String("i-0abc"),
				InstanceType:     ec2types.InstanceTypeT3Micro,
				Architecture:     ec2types.ArchitectureValuesX8664,
				ImageId:          aws.String("ami-123"),
				PrivateIpAddress: aws.String("10.0.0.5"),
				VpcId:            aws.String("vpc-1"),
				SubnetId:         aws.String("subnet-1"),
				Placement:        &ec2types.Placement{AvailabilityZone: aws.String("us-east-1a")},
				State:            &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
				LaunchTime:       &launch,
				Tags:             []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("web-01")}},
			},
			{
				// Terminated → must be skipped (absent).
				InstanceId: aws.String("i-dead"),
				State:      &ec2types.InstanceState{Name: ec2types.InstanceStateNameTerminated},
			},
		}}},
	}}

	stream := &captureStream[pluginv1.ObserveResponse]{ctx: context.Background()}
	if err := newServer(t, api).Observe(&pluginv1.ObserveRequest{}, stream); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("expected one ObserveResponse, got %d", len(stream.sent))
	}
	resp := stream.sent[0]
	if !resp.GetFullSyncComplete() {
		t.Error("full sync must set full_sync_complete for the tombstone boundary")
	}
	if len(resp.GetEntities()) != 1 {
		t.Fatalf("expected one live instance (terminated skipped), got %d", len(resp.GetEntities()))
	}
	e := resp.GetEntities()[0]
	if e.GetKind() != "instance" {
		t.Errorf("kind = %q, want instance", e.GetKind())
	}
	if e.GetIdentityKeys()["aws.instanceId"] != "i-0abc" {
		t.Errorf("identity: %v", e.GetIdentityKeys())
	}
	if e.GetLabels()["aws.region"] != "us-east-1" || e.GetLabels()["aws.name"] != "web-01" {
		t.Errorf("labels: %v", e.GetLabels())
	}
	for _, ns := range []string{"instance.compute", "instance.network", "instance.state"} {
		if len(e.GetFacets()[ns]) == 0 {
			t.Errorf("missing facet %q", ns)
		}
	}
	var compute map[string]any
	if err := json.Unmarshal(e.GetFacets()["instance.compute"], &compute); err != nil || compute["instanceType"] != "t3.micro" {
		t.Fatalf("instance.compute: %s %v", e.GetFacets()["instance.compute"], err)
	}
}

// TestInvokeCreateVM proves the Action half of the port (the FIRST Invoke plugin):
// RunInstances → a terminal InvokeResponse carrying the typed outputs (instanceId,
// privateIp) AND the new instance as an ObservedEntity. Result is set ONLY on the
// terminal event.
func TestInvokeCreateVM(t *testing.T) {
	api := &fakeEC2{runOut: &ec2.RunInstancesOutput{Instances: []ec2types.Instance{{
		InstanceId:       aws.String("i-999"),
		PrivateIpAddress: aws.String("10.0.0.9"),
	}}}}

	args, _ := json.Marshal(createVMParams{Region: "us-east-1", AMI: "ami-1", Name: "app-01"})
	req := &pluginv1.InvokeRequest{
		Action: "awsec2/create-vm",
		Args:   &pluginv1.Payload{Bytes: args},
	}
	stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
	if err := newServer(t, api).Invoke(req, stream); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(stream.sent) < 2 {
		t.Fatalf("expected a progress event then a terminal event, got %d", len(stream.sent))
	}

	// Only the final message is terminal and carries the Result.
	for i, resp := range stream.sent {
		last := i == len(stream.sent)-1
		if resp.GetEvent().GetTerminal() != last {
			t.Errorf("message %d terminal=%v, want %v", i, resp.GetEvent().GetTerminal(), last)
		}
		if !last && resp.GetResult() != nil {
			t.Errorf("non-terminal message %d must not carry Result", i)
		}
	}

	term := stream.sent[len(stream.sent)-1]
	if !term.GetEvent().GetOk() {
		t.Fatal("terminal event must be ok")
	}
	res := term.GetResult()
	if res == nil {
		t.Fatal("terminal message must carry Result")
	}
	var out map[string]any
	if err := json.Unmarshal(res.GetOutputs().GetBytes(), &out); err != nil {
		t.Fatalf("outputs: %v", err)
	}
	if out["instanceId"] != "i-999" || out["privateIp"] != "10.0.0.9" {
		t.Fatalf("outputs: %v", out)
	}
	if len(res.GetEntities()) != 1 {
		t.Fatalf("expected the provisioned instance entity, got %d", len(res.GetEntities()))
	}
	ent := res.GetEntities()[0]
	if ent.GetKind() != "instance" || ent.GetIdentityKeys()["aws.instanceId"] != "i-999" {
		t.Fatalf("entity: kind=%q identity=%v", ent.GetKind(), ent.GetIdentityKeys())
	}
	if ent.GetLabels()["aws.region"] != "us-east-1" {
		t.Fatalf("entity labels: %v", ent.GetLabels())
	}

	// The AMI + instanceType flowed into the RunInstances call, MinCount/MaxCount=1.
	if aws.ToString(api.lastRun.ImageId) != "ami-1" || api.lastRun.InstanceType != ec2types.InstanceTypeT3Micro {
		t.Errorf("run input: image=%q type=%q", aws.ToString(api.lastRun.ImageId), api.lastRun.InstanceType)
	}
	if aws.ToInt32(api.lastRun.MinCount) != 1 || aws.ToInt32(api.lastRun.MaxCount) != 1 {
		t.Errorf("run counts: min=%d max=%d", aws.ToInt32(api.lastRun.MinCount), aws.ToInt32(api.lastRun.MaxCount))
	}
}

// TestInvokeCreateVMProjectionOverlay proves the ADR-0058 §6 estate overlay: when
// the provisioning seam supplies projectKind + projectLabels, the built instance
// projects AS that kind with those labels merged onto aws.region — so a fleet View
// selects it and its stratt.intent/instance correlation resolves the provisioning
// Finding. Native identity (aws.instanceId) is preserved.
func TestInvokeCreateVMProjectionOverlay(t *testing.T) {
	api := &fakeEC2{runOut: &ec2.RunInstancesOutput{Instances: []ec2types.Instance{{
		InstanceId: aws.String("i-abc"),
	}}}}
	args, _ := json.Marshal(createVMParams{
		Region: "us-east-1", AMI: "ami-1", Name: "web-01",
		ProjectKind:   "host",
		ProjectLabels: map[string]string{"fleet": "web", "stratt.intent/instance": "web-01"},
	})
	stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
	if err := newServer(t, api).Invoke(&pluginv1.InvokeRequest{
		Action: "awsec2/create-vm", Args: &pluginv1.Payload{Bytes: args},
	}, stream); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	ent := stream.sent[len(stream.sent)-1].GetResult().GetEntities()[0]
	if ent.GetKind() != "host" {
		t.Errorf("projectKind overlay: kind=%q, want host", ent.GetKind())
	}
	if ent.GetIdentityKeys()["aws.instanceId"] != "i-abc" {
		t.Errorf("native identity must be preserved, got %v", ent.GetIdentityKeys())
	}
	l := ent.GetLabels()
	if l["fleet"] != "web" || l["stratt.intent/instance"] != "web-01" || l["aws.region"] != "us-east-1" {
		t.Errorf("overlay labels merged onto aws.region expected, got %v", l)
	}
}

// TestInvokeUnknownActionRejected — a content-blind selector that names no shipped
// Action is rejected, never guessed.
func TestInvokeUnknownActionRejected(t *testing.T) {
	stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
	err := newServer(t, &fakeEC2{}).Invoke(&pluginv1.InvokeRequest{Action: "awsec2/delete-everything"}, stream)
	if err == nil {
		t.Fatal("unknown action must be rejected")
	}
}

// TestGetManifest — the SYNCER class advertises OBSERVE + INVOKE, the 3 facet
// namespaces, the aws.instanceId tombstone scheme, and one create-vm ActionDecl.
func TestGetManifest(t *testing.T) {
	resp, err := newServer(t, &fakeEC2{}).GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	m := resp.GetManifest()
	if m.GetClass() != pluginv1.PluginClass_PLUGIN_CLASS_SYNCER {
		t.Errorf("class = %v", m.GetClass())
	}
	verbs := map[pluginv1.Verb]bool{}
	for _, v := range m.GetVerbs() {
		verbs[v] = true
	}
	if !verbs[pluginv1.Verb_VERB_OBSERVE] || !verbs[pluginv1.Verb_VERB_INVOKE] {
		t.Errorf("verbs = %v, want OBSERVE+INVOKE", m.GetVerbs())
	}
	if len(m.GetContracts()) != 3 {
		t.Errorf("expected 3 facet contracts, got %d", len(m.GetContracts()))
	}
	if len(m.GetTombstoneSchemes()) != 1 || m.GetTombstoneSchemes()[0] != "aws.instanceId" {
		t.Errorf("tombstone schemes = %v", m.GetTombstoneSchemes())
	}
	if len(m.GetActions()) != 1 {
		t.Fatalf("expected 1 action, got %d", len(m.GetActions()))
	}
	a := m.GetActions()[0]
	if a.GetName() != "awsec2/create-vm" || !a.GetDryRunnable() || a.GetIdempotent() {
		t.Errorf("action decl: name=%q dryRunnable=%v idempotent=%v", a.GetName(), a.GetDryRunnable(), a.GetIdempotent())
	}
	if a.GetInput().GetSchemaId() != "actions/awsec2/create-vm.input" || a.GetOutput().GetSchemaId() != "actions/awsec2/create-vm.output" {
		t.Errorf("contract refs: in=%q out=%q", a.GetInput().GetSchemaId(), a.GetOutput().GetSchemaId())
	}
}
