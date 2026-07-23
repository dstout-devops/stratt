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
	// Lifecycle + tag (ADR-0095): a single err drives the injected failure; the
	// last* fields record what the plugin sent.
	startOut  *ec2.StartInstancesOutput
	stopOut   *ec2.StopInstancesOutput
	termOut   *ec2.TerminateInstancesOutput
	opErr     error
	lastStart *ec2.StartInstancesInput
	lastStop  *ec2.StopInstancesInput
	lastReb   *ec2.RebootInstancesInput
	lastTerm  *ec2.TerminateInstancesInput
	lastTags  *ec2.CreateTagsInput
	// Resource creates (ADR-0095 C2).
	sgOut      *ec2.CreateSecurityGroupOutput
	keyOut     *ec2.ImportKeyPairOutput
	volOut     *ec2.CreateVolumeOutput
	vpcOut     *ec2.CreateVpcOutput
	subnetOut  *ec2.CreateSubnetOutput
	lastSG     *ec2.CreateSecurityGroupInput
	lastKey    *ec2.ImportKeyPairInput
	lastVol    *ec2.CreateVolumeInput
	lastVpc    *ec2.CreateVpcInput
	lastSubnet *ec2.CreateSubnetInput
	// Resource-graph observation (ADR-0096 C3).
	vpcsOut    *ec2.DescribeVpcsOutput
	subnetsOut *ec2.DescribeSubnetsOutput
	sgsOut     *ec2.DescribeSecurityGroupsOutput
	volsOut    *ec2.DescribeVolumesOutput
}

func (f *fakeEC2) DescribeVpcs(_ context.Context, _ *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	if f.vpcsOut == nil {
		return &ec2.DescribeVpcsOutput{}, f.describeErr
	}
	return f.vpcsOut, f.describeErr
}

func (f *fakeEC2) DescribeSubnets(_ context.Context, _ *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	if f.subnetsOut == nil {
		return &ec2.DescribeSubnetsOutput{}, f.describeErr
	}
	return f.subnetsOut, f.describeErr
}

func (f *fakeEC2) DescribeSecurityGroups(_ context.Context, _ *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	if f.sgsOut == nil {
		return &ec2.DescribeSecurityGroupsOutput{}, f.describeErr
	}
	return f.sgsOut, f.describeErr
}

func (f *fakeEC2) DescribeVolumes(_ context.Context, _ *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	if f.volsOut == nil {
		return &ec2.DescribeVolumesOutput{}, f.describeErr
	}
	return f.volsOut, f.describeErr
}

func (f *fakeEC2) DescribeInstances(_ context.Context, _ *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return f.describeOut, f.describeErr
}

func (f *fakeEC2) RunInstances(_ context.Context, in *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	f.lastRun = in
	return f.runOut, f.runErr
}

func (f *fakeEC2) StartInstances(_ context.Context, in *ec2.StartInstancesInput, _ ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	f.lastStart = in
	return f.startOut, f.opErr
}

func (f *fakeEC2) StopInstances(_ context.Context, in *ec2.StopInstancesInput, _ ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
	f.lastStop = in
	return f.stopOut, f.opErr
}

func (f *fakeEC2) RebootInstances(_ context.Context, in *ec2.RebootInstancesInput, _ ...func(*ec2.Options)) (*ec2.RebootInstancesOutput, error) {
	f.lastReb = in
	return &ec2.RebootInstancesOutput{}, f.opErr
}

func (f *fakeEC2) TerminateInstances(_ context.Context, in *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	f.lastTerm = in
	return f.termOut, f.opErr
}

func (f *fakeEC2) CreateTags(_ context.Context, in *ec2.CreateTagsInput, _ ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {
	f.lastTags = in
	return &ec2.CreateTagsOutput{}, f.opErr
}

func (f *fakeEC2) CreateSecurityGroup(_ context.Context, in *ec2.CreateSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error) {
	f.lastSG = in
	return f.sgOut, f.opErr
}

func (f *fakeEC2) ImportKeyPair(_ context.Context, in *ec2.ImportKeyPairInput, _ ...func(*ec2.Options)) (*ec2.ImportKeyPairOutput, error) {
	f.lastKey = in
	return f.keyOut, f.opErr
}

func (f *fakeEC2) CreateVolume(_ context.Context, in *ec2.CreateVolumeInput, _ ...func(*ec2.Options)) (*ec2.CreateVolumeOutput, error) {
	f.lastVol = in
	return f.volOut, f.opErr
}

func (f *fakeEC2) CreateVpc(_ context.Context, in *ec2.CreateVpcInput, _ ...func(*ec2.Options)) (*ec2.CreateVpcOutput, error) {
	f.lastVpc = in
	return f.vpcOut, f.opErr
}

func (f *fakeEC2) CreateSubnet(_ context.Context, in *ec2.CreateSubnetInput, _ ...func(*ec2.Options)) (*ec2.CreateSubnetOutput, error) {
	f.lastSubnet = in
	return f.subnetOut, f.opErr
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

// TestObserveEmitsResourceGraph proves the C3 Syncer enumerates the full resource graph
// (ADR-0096): instances PLUS vpc/subnet/security-group/volume, each with its identity
// scheme + Facet, in the one full-sync.
func TestObserveEmitsResourceGraph(t *testing.T) {
	api := &fakeEC2{
		describeOut: &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{
			InstanceId: aws.String("i-1"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
		}}}}},
		vpcsOut:    &ec2.DescribeVpcsOutput{Vpcs: []ec2types.Vpc{{VpcId: aws.String("vpc-1"), CidrBlock: aws.String("10.0.0.0/16")}}},
		subnetsOut: &ec2.DescribeSubnetsOutput{Subnets: []ec2types.Subnet{{SubnetId: aws.String("subnet-1"), VpcId: aws.String("vpc-1"), CidrBlock: aws.String("10.0.1.0/24")}}},
		sgsOut:     &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{{GroupId: aws.String("sg-1"), GroupName: aws.String("web"), VpcId: aws.String("vpc-1")}}},
		volsOut:    &ec2.DescribeVolumesOutput{Volumes: []ec2types.Volume{{VolumeId: aws.String("vol-1"), Size: aws.Int32(8)}}},
	}
	stream := &captureStream[pluginv1.ObserveResponse]{ctx: context.Background()}
	if err := newServer(t, api).Observe(&pluginv1.ObserveRequest{}, stream); err != nil {
		t.Fatalf("observe: %v", err)
	}
	byKind := map[string]*pluginv1.ObservedEntity{}
	for _, resp := range stream.sent {
		for _, e := range resp.GetEntities() {
			byKind[e.GetKind()] = e
		}
	}
	for _, k := range []string{"instance", "vpc", "subnet", "security-group", "volume"} {
		if byKind[k] == nil {
			t.Fatalf("Observe must emit a %q Entity; got kinds %v", k, kindKeys(byKind))
		}
	}
	if byKind["vpc"].GetIdentityKeys()["aws.vpcId"] != "vpc-1" {
		t.Errorf("vpc identity: %v", byKind["vpc"].GetIdentityKeys())
	}
	// subnet carries its in-vpc Relation.
	rels := byKind["subnet"].GetRelations()
	if len(rels) != 1 || rels[0].GetType() != "in-vpc" || rels[0].GetToValue() != "vpc-1" {
		t.Errorf("subnet in-vpc relation: %+v", rels)
	}
	if len(byKind["volume"].GetFacets()["storage.volume"]) == 0 {
		t.Error("volume must carry a storage.volume facet")
	}
}

func kindKeys(m map[string]*pluginv1.ObservedEntity) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
	// instance.{compute,network,state} + net.{vpc,subnet,securitygroup} + storage.volume.
	if len(m.GetContracts()) != 7 {
		t.Errorf("expected 7 facet contracts, got %d", len(m.GetContracts()))
	}
	// provisioning capability (ADR-0107) — advertised so provider verification (ADR-0104 D1) can bind it.
	var provisions bool
	for _, c := range m.GetCapabilities() {
		if c == "provisioning" {
			provisions = true
		}
	}
	if !provisions {
		t.Errorf("manifest must advertise the provisioning capability, got %v", m.GetCapabilities())
	}
	// One tombstone scheme per Observed Entity kind (instance + the 4 resource kinds).
	tomb := map[string]bool{}
	for _, s := range m.GetTombstoneSchemes() {
		tomb[s] = true
	}
	for _, want := range []string{"aws.instanceId", "aws.vpcId", "aws.subnetId", "aws.securityGroupId", "aws.volumeId"} {
		if !tomb[want] {
			t.Errorf("missing tombstone scheme %q (got %v)", want, m.GetTombstoneSchemes())
		}
	}
	// create-vm + start/stop/reboot/terminate/tag (ADR-0095).
	byName := map[string]*pluginv1.ActionDecl{}
	for _, a := range m.GetActions() {
		byName[a.GetName()] = a
	}
	for _, want := range []string{
		"awsec2/create-vm", "awsec2/start", "awsec2/stop", "awsec2/reboot", "awsec2/terminate", "awsec2/tag",
		"awsec2/create-security-group", "awsec2/import-key-pair", "awsec2/create-volume", "awsec2/create-vpc", "awsec2/create-subnet",
	} {
		a := byName[want]
		if a == nil {
			t.Fatalf("missing ActionDecl %q (got %d actions)", want, len(m.GetActions()))
		}
		op := want[len("awsec2/"):]
		if a.GetInput().GetSchemaId() != "actions/awsec2/"+op+".input" || a.GetOutput().GetSchemaId() != "actions/awsec2/"+op+".output" {
			t.Errorf("%s contract refs: in=%q out=%q", want, a.GetInput().GetSchemaId(), a.GetOutput().GetSchemaId())
		}
		if !a.GetDryRunnable() {
			t.Errorf("%s must be DryRunnable", want)
		}
	}
	// reboot has no stable end-state ⇒ not idempotent; the others are.
	if byName["awsec2/reboot"].GetIdempotent() {
		t.Error("reboot must not be marked idempotent")
	}
	if !byName["awsec2/start"].GetIdempotent() {
		t.Error("start must be idempotent (starting a running instance is a no-op)")
	}
}

// TestInvokeLifecycle proves the lifecycle Actions: each drives its EC2 call with the
// target instance id + DryRun, and projects ONLY instance.state with Run provenance
// (ADR-0095 flag 3) — never compute/network (the Syncer owns those).
func TestInvokeLifecycle(t *testing.T) {
	cases := []struct {
		action, wantState string
		setup             func(*fakeEC2)
		check             func(*testing.T, *fakeEC2)
	}{
		{"awsec2/start", "pending", func(f *fakeEC2) {
			f.startOut = &ec2.StartInstancesOutput{StartingInstances: []ec2types.InstanceStateChange{{CurrentState: &ec2types.InstanceState{Name: ec2types.InstanceStateNamePending}}}}
		}, func(t *testing.T, f *fakeEC2) {
			if f.lastStart == nil || f.lastStart.InstanceIds[0] != "i-1" {
				t.Fatalf("start not called with i-1: %+v", f.lastStart)
			}
		}},
		{"awsec2/stop", "stopping", func(f *fakeEC2) {
			f.stopOut = &ec2.StopInstancesOutput{StoppingInstances: []ec2types.InstanceStateChange{{CurrentState: &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopping}}}}
		}, func(t *testing.T, f *fakeEC2) {
			if f.lastStop == nil || f.lastStop.InstanceIds[0] != "i-1" {
				t.Fatalf("stop not called with i-1")
			}
		}},
		{"awsec2/terminate", "shutting-down", func(f *fakeEC2) {
			f.termOut = &ec2.TerminateInstancesOutput{TerminatingInstances: []ec2types.InstanceStateChange{{CurrentState: &ec2types.InstanceState{Name: ec2types.InstanceStateNameShuttingDown}}}}
		}, func(t *testing.T, f *fakeEC2) {
			if f.lastTerm == nil || f.lastTerm.InstanceIds[0] != "i-1" {
				t.Fatalf("terminate not called with i-1")
			}
		}},
		{"awsec2/reboot", "", func(f *fakeEC2) {}, func(t *testing.T, f *fakeEC2) {
			if f.lastReb == nil || f.lastReb.InstanceIds[0] != "i-1" {
				t.Fatalf("reboot not called with i-1")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			api := &fakeEC2{}
			tc.setup(api)
			args, _ := json.Marshal(lifecycleParams{InstanceID: "i-1"})
			stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
			if err := newServer(t, api).Invoke(&pluginv1.InvokeRequest{Action: tc.action, Args: &pluginv1.Payload{Bytes: args}}, stream); err != nil {
				t.Fatalf("invoke: %v", err)
			}
			tc.check(t, api)
			term := stream.sent[len(stream.sent)-1]
			if !term.GetEvent().GetTerminal() || !term.GetEvent().GetOk() {
				t.Fatalf("expected terminal ok, got %+v", term.GetEvent())
			}
			op := tc.action[len("awsec2/"):]
			if term.GetResult().GetOutputContract().GetSchemaId() != "actions/awsec2/"+op+".output" {
				t.Errorf("output contract: %q", term.GetResult().GetOutputContract().GetSchemaId())
			}
			ents := term.GetResult().GetEntities()
			if tc.wantState == "" {
				if len(ents) != 0 {
					t.Fatalf("reboot must project no state entity, got %d", len(ents))
				}
				return
			}
			if len(ents) != 1 {
				t.Fatalf("expected one instance.state projection, got %d", len(ents))
			}
			e := ents[0]
			if e.GetKind() != "instance" || e.GetIdentityKeys()["aws.instanceId"] != "i-1" {
				t.Fatalf("entity identity: %+v", e)
			}
			// ONLY instance.state — never compute/network (flag 3).
			if len(e.GetFacets()) != 1 || len(e.GetFacets()["instance.state"]) == 0 {
				t.Fatalf("lifecycle must project ONLY instance.state, got facets %v", facetKeys(e.GetFacets()))
			}
			var st map[string]any
			if err := json.Unmarshal(e.GetFacets()["instance.state"], &st); err != nil || st["state"] != tc.wantState {
				t.Fatalf("state facet = %s (want %s), err=%v", e.GetFacets()["instance.state"], tc.wantState, err)
			}
		})
	}
}

// TestInvokeTag proves awsec2/tag maps tags → CreateTags and returns a count, with no
// Run-provenance Entity (the Syncer reflects tags on its next poll, §1.2).
func TestInvokeTag(t *testing.T) {
	api := &fakeEC2{}
	args, _ := json.Marshal(tagParams{InstanceID: "i-1", Tags: map[string]string{"env": "dev", "team": "platform"}})
	stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
	if err := newServer(t, api).Invoke(&pluginv1.InvokeRequest{Action: "awsec2/tag", Args: &pluginv1.Payload{Bytes: args}}, stream); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if api.lastTags == nil || len(api.lastTags.Tags) != 2 || api.lastTags.Resources[0] != "i-1" {
		t.Fatalf("CreateTags not called correctly: %+v", api.lastTags)
	}
	term := stream.sent[len(stream.sent)-1]
	if !term.GetEvent().GetOk() || len(term.GetResult().GetEntities()) != 0 {
		t.Fatalf("tag must be terminal-ok with no entity: %+v", term)
	}
	var out map[string]any
	_ = json.Unmarshal(term.GetResult().GetOutputs().GetBytes(), &out)
	if out["tagged"].(float64) != 2 {
		t.Fatalf("tagged count = %v", out["tagged"])
	}
}

// TestLifecycleRequiresInstanceID — a lifecycle Action with no instanceId is rejected.
func TestLifecycleRequiresInstanceID(t *testing.T) {
	stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
	if err := newServer(t, &fakeEC2{}).Invoke(&pluginv1.InvokeRequest{Action: "awsec2/stop"}, stream); err == nil {
		t.Fatal("stop without instanceId must be rejected")
	}
}

func facetKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
