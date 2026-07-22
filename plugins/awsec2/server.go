package awsec2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	smithy "github.com/aws/smithy-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Action names this plugin advertises (ActionDecl.name); the InvokeRequest.action
// selector picks one. "" is accepted as the create-vm default (back-compat).
const (
	actionCreateVM  = "awsec2/create-vm"
	actionStart     = "awsec2/start"
	actionStop      = "awsec2/stop"
	actionReboot    = "awsec2/reboot"
	actionTerminate = "awsec2/terminate"
	actionTag       = "awsec2/tag"
	// Resource-provisioning Actions (ADR-0095 C2, fire-and-return). Each stamps a
	// stratt-owned marker tag at creation so the C3 Syncer / an orphan scan can find
	// what the platform made (guardian flag 1 — no silent billable leak).
	actionCreateSG     = "awsec2/create-security-group"
	actionImportKey    = "awsec2/import-key-pair" // ImportKeyPair (public key) — NEVER CreateKeyPair, whose private-key return would cross the core (§2.5)
	actionCreateVolume = "awsec2/create-volume"
	actionCreateVPC    = "awsec2/create-vpc"
	actionCreateSubnet = "awsec2/create-subnet"
)

// Facet namespaces this Syncer REQUESTS to own (§2.1); the core honors them only
// where the operator grant allows.
var facetNamespaces = []string{
	"instance.compute", // instanceType, architecture, imageId
	"instance.network", // ips, vpc, subnet, az
	"instance.state",   // lifecycle state, launch time
	// Resource-graph Facets (ADR-0096). net.subnet is co-owned with crossplane/NetBox
	// (multi-source, ADR-0060) — awsec2 is NOT authoritative for it.
	"net.vpc",
	"net.subnet",
	"net.securitygroup",
	"storage.volume",
}

// tombstoneSchemes are the identity schemes this Syncer fully enumerates — one per
// Observed Entity kind, so the host tombstones absent objects on each full-sync (ADR-0096).
var tombstoneSchemes = []string{
	"aws.instanceId", "aws.vpcId", "aws.subnetId", "aws.securityGroupId", "aws.volumeId",
}

// EC2API is the small slice of the EC2 API this plugin needs — DescribeInstances
// for the Syncer, RunInstances for the create-vm Action. Abstracting it behind an
// interface lets tests inject fakes with no AWS calls (the ADR-0046 module-isolation
// proof: the plugin's content-expertise is exercised in isolation). *ec2.Client
// satisfies it; so does ec2.DescribeInstancesAPIClient for the paginator.
type EC2API interface {
	DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	RunInstances(ctx context.Context, in *ec2.RunInstancesInput, opts ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
	// Lifecycle (ADR-0095): each drives a real instance state transition.
	StartInstances(ctx context.Context, in *ec2.StartInstancesInput, opts ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error)
	StopInstances(ctx context.Context, in *ec2.StopInstancesInput, opts ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error)
	RebootInstances(ctx context.Context, in *ec2.RebootInstancesInput, opts ...func(*ec2.Options)) (*ec2.RebootInstancesOutput, error)
	TerminateInstances(ctx context.Context, in *ec2.TerminateInstancesInput, opts ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
	CreateTags(ctx context.Context, in *ec2.CreateTagsInput, opts ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error)
	// Resource provisioning (ADR-0095 C2, fire-and-return). ImportKeyPair — NOT
	// CreateKeyPair — because CreateKeyPair returns generated PRIVATE key material,
	// which must never cross the core (§2.5); import takes a caller public key.
	CreateSecurityGroup(ctx context.Context, in *ec2.CreateSecurityGroupInput, opts ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error)
	ImportKeyPair(ctx context.Context, in *ec2.ImportKeyPairInput, opts ...func(*ec2.Options)) (*ec2.ImportKeyPairOutput, error)
	CreateVolume(ctx context.Context, in *ec2.CreateVolumeInput, opts ...func(*ec2.Options)) (*ec2.CreateVolumeOutput, error)
	CreateVpc(ctx context.Context, in *ec2.CreateVpcInput, opts ...func(*ec2.Options)) (*ec2.CreateVpcOutput, error)
	CreateSubnet(ctx context.Context, in *ec2.CreateSubnetInput, opts ...func(*ec2.Options)) (*ec2.CreateSubnetOutput, error)
	// Resource-graph observation (ADR-0096 C3): the Syncer enumerates each kind as a
	// full-sync alongside instances.
	DescribeVpcs(ctx context.Context, in *ec2.DescribeVpcsInput, opts ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(ctx context.Context, in *ec2.DescribeSubnetsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
	DescribeVolumes(ctx context.Context, in *ec2.DescribeVolumesInput, opts ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
}

// Config locates the EC2 Source. Credentials arrive resolved from the plugin's OWN
// broker at spawn via the SDK's standard chain (AWS_ACCESS_KEY_ID etc.); material
// never crosses the core (§2.5).
type Config struct {
	PluginID string // the authenticated channel identity the operator grant is keyed on
	Endpoint string // API endpoint override (dev: the moto stand-in)
	Region   string
}

// Server implements the sovereign plugin port for the awsec2 Connector — a
// SYNCER-class plugin advertising OBSERVE (the instance Syncer) AND INVOKE (the
// create-vm Action). It advertises the facet namespaces + tombstone schemes it
// REQUESTS to own and the Actions it ships; the core-side host honors them only
// where the operator grant allows.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg Config
	log *slog.Logger
	// newAPI builds the EC2 client; overridable in tests to inject a fake.
	newAPI func(context.Context) (EC2API, error)
}

func NewServer(cfg Config, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "awsec2"
	}
	s := &Server{cfg: cfg, log: log.With("plugin", "awsec2")}
	s.newAPI = s.buildClient
	return s
}

// buildClient constructs the real EC2 client from the standard config chain.
func (s *Server) buildClient(ctx context.Context) (EC2API, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(s.cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("awsec2: load config: %w", err)
	}
	return ec2.NewFromConfig(cfg, func(o *ec2.Options) {
		if s.cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(s.cfg.Endpoint)
		}
	}), nil
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	contracts := make([]*pluginv1.ContractDecl, 0, len(facetNamespaces))
	for _, ns := range facetNamespaces {
		contracts = append(contracts, &pluginv1.ContractDecl{SchemaId: ns})
	}
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:         s.cfg.PluginID,
		ProtocolVersion:  "v1",
		Class:            pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:            []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE, pluginv1.Verb_VERB_INVOKE},
		Contracts:        contracts,
		TombstoneSchemes: tombstoneSchemes,
		Actions: []*pluginv1.ActionDecl{
			{
				Name:        actionCreateVM,
				Input:       &pluginv1.ContractRef{SchemaId: "actions/awsec2/create-vm.input"},
				Output:      &pluginv1.ContractRef{SchemaId: "actions/awsec2/create-vm.output"},
				Idempotent:  false, // each call provisions a new instance
				DryRunnable: true,  // RunInstances supports DryRun
			},
			// Lifecycle Actions (ADR-0095): idempotent w.r.t. the target END state
			// (starting a running instance is a no-op), each DryRunnable via the EC2
			// API's own DryRun. reboot has no stable end-state to assert idempotent.
			lifecycleDecl(actionStart, true),
			lifecycleDecl(actionStop, true),
			lifecycleDecl(actionReboot, false),
			lifecycleDecl(actionTerminate, true),
			lifecycleDecl(actionTag, true),
			// Resource-provisioning Actions (C2). Not idempotent — each call creates a
			// new object; DryRunnable via the EC2 API's own dry-run.
			lifecycleDecl(actionCreateSG, false),
			lifecycleDecl(actionImportKey, false),
			lifecycleDecl(actionCreateVolume, false),
			lifecycleDecl(actionCreateVPC, false),
			lifecycleDecl(actionCreateSubnet, false),
		},
	}}, nil
}

// lifecycleDecl builds an ActionDecl for an instance-scoped Action whose input+output
// contracts follow the actions/awsec2/<op>.{input,output} convention.
func lifecycleDecl(name string, idempotent bool) *pluginv1.ActionDecl {
	op := name[len("awsec2/"):]
	return &pluginv1.ActionDecl{
		Name:        name,
		Input:       &pluginv1.ContractRef{SchemaId: "actions/awsec2/" + op + ".input"},
		Output:      &pluginv1.ContractRef{SchemaId: "actions/awsec2/" + op + ".output"},
		Idempotent:  idempotent,
		DryRunnable: true,
	}
}

// Observe performs a full sync: EC2 exposes no change feed, so each cycle is an
// honest paginated full enumeration streamed as ObservedEntities with the
// full_sync_complete boundary so the host can tombstone absent instances
// (ADR-0042). Terminated instances (EC2 keeps them visible for a while) count as
// absent and are skipped.
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	ctx := stream.Context()
	api, err := s.newAPI(ctx)
	if err != nil {
		return err
	}
	entities, err := observe(ctx, api, s.cfg.Region)
	if err != nil {
		return err
	}
	s.log.Info("full sync", "instances", len(entities))
	return stream.Send(&pluginv1.ObserveResponse{Entities: entities, FullSyncComplete: true})
}

// observe enumerates the full resource graph (ADR-0096): instances PLUS vpc / subnet /
// security-group / volume, each a full enumeration appended to the one full-sync so the
// host tombstones absent objects per identity scheme. Pure content-expertise; no graph
// writes (the plugin holds no DB path). A per-kind failure fails the whole sync (§1.8).
func observe(ctx context.Context, api EC2API, region string) ([]*pluginv1.ObservedEntity, error) {
	out, err := observeInstances(ctx, api, region)
	if err != nil {
		return nil, err
	}
	for _, enum := range []func(context.Context, EC2API, string) ([]*pluginv1.ObservedEntity, error){
		observeVPCs, observeSubnets, observeSecurityGroups, observeVolumes,
	} {
		es, err := enum(ctx, api, region)
		if err != nil {
			return nil, err
		}
		out = append(out, es...)
	}
	return out, nil
}

// observeInstances paginates DescribeInstances and normalizes each live instance.
func observeInstances(ctx context.Context, api EC2API, region string) ([]*pluginv1.ObservedEntity, error) {
	var out []*pluginv1.ObservedEntity
	pager := ec2.NewDescribeInstancesPaginator(api, &ec2.DescribeInstancesInput{})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("awsec2: describe instances: %w", err)
		}
		for _, r := range page.Reservations {
			for _, in := range r.Instances {
				if in.State != nil && in.State.Name == ec2types.InstanceStateNameTerminated {
					continue
				}
				e, err := normalizeInstance(region, in)
				if err != nil {
					continue
				}
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// createVMParams is the input Contract (actions/awsec2/create-vm.input). AWS creds
// are NOT here — resolved from the plugin's own broker as a CredentialRef (§2.5).
type createVMParams struct {
	Region       string `json:"region"`
	Endpoint     string `json:"endpoint"`
	InstanceType string `json:"instanceType"`
	AMI          string `json:"ami"`
	Name         string `json:"name"`
	// ProjectKind/ProjectLabels are the estate OVERLAY the provisioning seam
	// declares (ADR-0058 decision 6): the built instance projects AS this kind
	// with these labels — carried on the Action's own Run-provenance output
	// (never a reconcile write, §1.2). Empty ProjectKind keeps the native
	// "instance". The labels must be keys the Run may own (§2.1 per-key) — the
	// stratt.intent/instance correlation key + fleet keys, not another Source's.
	ProjectKind   string            `json:"projectKind"`
	ProjectLabels map[string]string `json:"projectLabels"`
}

// Invoke runs the create-vm Action: provision one EC2 instance as a single typed
// operation and stream typed TaskEvents, ending with a TERMINAL InvokeResponse
// carrying the InvokeResult (outputs + the new instance as an ObservedEntity with
// Run provenance, §1.2). Result is set ONLY on the terminal message.
func (s *Server) Invoke(req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	ctx := stream.Context()
	api, err := s.newAPI(ctx)
	if err != nil {
		return err
	}
	// Content-blind dispatch: an action the plugin does not ship is rejected, never
	// guessed (§1.5). "" defaults to create-vm for back-compat with the first slice.
	switch req.GetAction() {
	case "", actionCreateVM:
		return s.invokeCreateVM(ctx, req, stream, api)
	case actionStart, actionStop, actionReboot, actionTerminate:
		return s.invokeLifecycle(ctx, req, stream, api)
	case actionTag:
		return s.invokeTag(ctx, req, stream, api)
	case actionCreateSG, actionImportKey, actionCreateVolume, actionCreateVPC, actionCreateSubnet:
		return s.invokeCreateResource(ctx, req, stream, api)
	default:
		return status.Errorf(codes.InvalidArgument, "awsec2: unknown action %q", req.GetAction())
	}
}

// invokeCreateVM provisions one EC2 instance and projects it with Run provenance —
// the original create-vm Action (ADR-0058 provisioning seam).
func (s *Server) invokeCreateVM(ctx context.Context, req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], api EC2API) error {
	var p createVMParams
	if args := req.GetArgs(); args != nil && len(args.GetBytes()) > 0 {
		if err := json.Unmarshal(args.GetBytes(), &p); err != nil {
			return status.Errorf(codes.InvalidArgument, "awsec2/create-vm: invalid args: %v", err)
		}
	}
	if p.Region == "" || p.AMI == "" {
		return status.Errorf(codes.InvalidArgument, "awsec2/create-vm requires region and ami")
	}
	if p.InstanceType == "" {
		p.InstanceType = "t3.micro"
	}

	// Progress event (typed, core-legible descent — §1.8).
	if err := stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level:         pluginv1.TaskEvent_LEVEL_INFO,
		Message:       fmt.Sprintf("provisioning ec2 instance (%s) from %s", p.InstanceType, p.AMI),
		At:            timestamppb.Now(),
		CorrelationId: req.GetEnvelope().GetCorrelationId(),
		Fields:        map[string]string{"region": p.Region, "instanceType": p.InstanceType, "ami": p.AMI},
	}}); err != nil {
		return err
	}

	input := &ec2.RunInstancesInput{
		ImageId:      aws.String(p.AMI),
		InstanceType: ec2types.InstanceType(p.InstanceType),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		DryRun:       aws.Bool(req.GetDryRun()),
	}
	if p.Name != "" {
		input.TagSpecifications = []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeInstance,
			Tags:         []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String(p.Name)}},
		}}
	}

	out, err := api.RunInstances(ctx, input)
	if err != nil {
		// A DryRun that passes the permission check surfaces as a DryRunOperation
		// API error — that IS the plan succeeding. No instance was created, so the
		// terminal result carries no bindable outputs and no Entity.
		if req.GetDryRun() && isDryRunSuccess(err) {
			return stream.Send(&pluginv1.InvokeResponse{
				Event: &pluginv1.TaskEvent{
					Level:         pluginv1.TaskEvent_LEVEL_INFO,
					Message:       "dry-run ok: would provision one instance",
					At:            timestamppb.Now(),
					CorrelationId: req.GetEnvelope().GetCorrelationId(),
					Terminal:      true,
					Ok:            true,
				},
				Result: &pluginv1.InvokeResult{
					OutputContract: &pluginv1.ContractRef{SchemaId: "actions/awsec2/create-vm.output"},
				},
			})
		}
		return s.terminalFailure(stream, req, fmt.Errorf("awsec2/create-vm: run instances: %w", err))
	}
	if len(out.Instances) == 0 {
		return s.terminalFailure(stream, req, errors.New("awsec2/create-vm: RunInstances returned no instance"))
	}

	inst := out.Instances[0]
	instanceID := aws.ToString(inst.InstanceId)
	privateIP := aws.ToString(inst.PrivateIpAddress)
	if instanceID == "" {
		return s.terminalFailure(stream, req, errors.New("awsec2/create-vm: provisioned instance has no id"))
	}

	// Typed outputs (actions/awsec2/create-vm.output).
	outputs, err := json.Marshal(map[string]any{"instanceId": instanceID, "privateIp": privateIP})
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("awsec2/create-vm: marshal outputs: %w", err))
	}

	// Project the new instance with Run provenance (§1.2). The Action declares
	// identity + region; when the provisioning seam supplied a projectKind/labels
	// overlay (ADR-0058 decision 6), the instance projects AS that estate kind
	// with those labels — so a fleet View selects it and its provisioning Finding
	// resolves. Facets still arrive from the awsec2 Syncer's next poll.
	kind := "instance"
	if p.ProjectKind != "" {
		kind = p.ProjectKind
	}
	labels := map[string]string{"aws.region": p.Region}
	for k, v := range p.ProjectLabels {
		labels[k] = v
	}
	entity := &pluginv1.ObservedEntity{
		Kind:         kind,
		IdentityKeys: map[string]string{"aws.instanceId": instanceID},
		Labels:       labels,
	}

	s.log.Info("provisioned instance", "instanceId", instanceID, "privateIp", privateIP)
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level:         pluginv1.TaskEvent_LEVEL_INFO,
			Message:       "provisioned " + instanceID,
			At:            timestamppb.Now(),
			CorrelationId: req.GetEnvelope().GetCorrelationId(),
			Fields:        map[string]string{"instanceId": instanceID, "privateIp": privateIP},
			Terminal:      true,
			Ok:            true,
		},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: outputs},
			OutputContract: &pluginv1.ContractRef{SchemaId: "actions/awsec2/create-vm.output"},
			Entities:       []*pluginv1.ObservedEntity{entity},
		},
	})
}

// lifecycleParams is the input for the instance lifecycle Actions (start/stop/reboot/
// terminate): the instance to act on, by its native id.
type lifecycleParams struct {
	InstanceID string `json:"instanceId"`
}

// lifecycleOutput is the bindable output of a lifecycle Action: the instance id and
// its resulting provider state (empty for reboot, which has no state-change response).
type lifecycleOutput struct {
	InstanceID string `json:"instanceId"`
	State      string `json:"state,omitempty"`
}

// invokeLifecycle drives a real instance state transition and projects the instance
// with Run provenance — ONLY the instance.state Facet it authoritatively affects
// (ADR-0095 flag 3); the Syncer owns compute/network. DryRun rides the EC2 API's own
// dry-run (the DryRunOperation signal is plan-success).
func (s *Server) invokeLifecycle(ctx context.Context, req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], api EC2API) error {
	action := req.GetAction()
	op := action[len("awsec2/"):]
	var p lifecycleParams
	if args := req.GetArgs(); args != nil && len(args.GetBytes()) > 0 {
		if err := json.Unmarshal(args.GetBytes(), &p); err != nil {
			return status.Errorf(codes.InvalidArgument, "%s: invalid args: %v", action, err)
		}
	}
	if p.InstanceID == "" {
		return status.Errorf(codes.InvalidArgument, "%s requires instanceId", action)
	}
	if err := stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level:         pluginv1.TaskEvent_LEVEL_INFO,
		Message:       fmt.Sprintf("%s %s", op, p.InstanceID),
		At:            timestamppb.Now(),
		CorrelationId: req.GetEnvelope().GetCorrelationId(),
		Fields:        map[string]string{"instanceId": p.InstanceID},
	}}); err != nil {
		return err
	}

	dry := aws.Bool(req.GetDryRun())
	ids := []string{p.InstanceID}
	var newState string
	var err error
	switch action {
	case actionStart:
		out, e := api.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: ids, DryRun: dry})
		if err = e; e == nil && len(out.StartingInstances) > 0 {
			newState = string(out.StartingInstances[0].CurrentState.Name)
		}
	case actionStop:
		out, e := api.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: ids, DryRun: dry})
		if err = e; e == nil && len(out.StoppingInstances) > 0 {
			newState = string(out.StoppingInstances[0].CurrentState.Name)
		}
	case actionReboot:
		// reboot returns no state change — the instance stays running.
		_, err = api.RebootInstances(ctx, &ec2.RebootInstancesInput{InstanceIds: ids, DryRun: dry})
	case actionTerminate:
		out, e := api.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: ids, DryRun: dry})
		if err = e; e == nil && len(out.TerminatingInstances) > 0 {
			newState = string(out.TerminatingInstances[0].CurrentState.Name)
		}
	}
	if err != nil {
		if req.GetDryRun() && isDryRunSuccess(err) {
			return s.terminalDryRun(stream, req, fmt.Sprintf("dry-run ok: would %s %s", op, p.InstanceID), "actions/awsec2/"+op+".output")
		}
		return s.terminalFailure(stream, req, fmt.Errorf("%s: %w", action, err))
	}

	outputs, err := json.Marshal(lifecycleOutput{InstanceID: p.InstanceID, State: newState})
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("%s: marshal outputs: %w", action, err))
	}
	// Project ONLY instance.state with Run provenance (flag 3) — and only when the
	// transition yielded an observable state (not reboot).
	var entities []*pluginv1.ObservedEntity
	if newState != "" {
		stateDoc, merr := json.Marshal(map[string]any{"state": newState})
		if merr != nil {
			return s.terminalFailure(stream, req, fmt.Errorf("%s: marshal state facet: %w", action, merr))
		}
		entities = []*pluginv1.ObservedEntity{{
			Kind:         "instance",
			IdentityKeys: map[string]string{"aws.instanceId": p.InstanceID},
			Facets:       map[string][]byte{"instance.state": stateDoc},
		}}
	}
	s.log.Info("lifecycle", "op", op, "instanceId", p.InstanceID, "state", newState)
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level:         pluginv1.TaskEvent_LEVEL_INFO,
			Message:       fmt.Sprintf("%s %s ok", op, p.InstanceID),
			At:            timestamppb.Now(),
			CorrelationId: req.GetEnvelope().GetCorrelationId(),
			Fields:        map[string]string{"instanceId": p.InstanceID, "state": newState},
			Terminal:      true,
			Ok:            true,
		},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: outputs},
			OutputContract: &pluginv1.ContractRef{SchemaId: "actions/awsec2/" + op + ".output"},
			Entities:       entities,
		},
	})
}

// tagParams is the input for awsec2/tag: the instance and the tags to set on it.
type tagParams struct {
	InstanceID string            `json:"instanceId"`
	Tags       map[string]string `json:"tags"`
}

// invokeTag sets tags on an instance (CreateTags). Tags surface as labels on the next
// Syncer poll (the Syncer owns the projection, §1.2), so this Action returns outputs
// only — no Run-provenance Entity write.
func (s *Server) invokeTag(ctx context.Context, req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], api EC2API) error {
	var p tagParams
	if args := req.GetArgs(); args != nil && len(args.GetBytes()) > 0 {
		if err := json.Unmarshal(args.GetBytes(), &p); err != nil {
			return status.Errorf(codes.InvalidArgument, "awsec2/tag: invalid args: %v", err)
		}
	}
	if p.InstanceID == "" || len(p.Tags) == 0 {
		return status.Errorf(codes.InvalidArgument, "awsec2/tag requires instanceId and at least one tag")
	}
	tags := make([]ec2types.Tag, 0, len(p.Tags))
	for k, v := range p.Tags {
		tags = append(tags, ec2types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	if err := stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level:         pluginv1.TaskEvent_LEVEL_INFO,
		Message:       fmt.Sprintf("tagging %s (%d tags)", p.InstanceID, len(tags)),
		At:            timestamppb.Now(),
		CorrelationId: req.GetEnvelope().GetCorrelationId(),
		Fields:        map[string]string{"instanceId": p.InstanceID},
	}}); err != nil {
		return err
	}
	_, err := api.CreateTags(ctx, &ec2.CreateTagsInput{Resources: []string{p.InstanceID}, Tags: tags, DryRun: aws.Bool(req.GetDryRun())})
	if err != nil {
		if req.GetDryRun() && isDryRunSuccess(err) {
			return s.terminalDryRun(stream, req, fmt.Sprintf("dry-run ok: would tag %s", p.InstanceID), "actions/awsec2/tag.output")
		}
		return s.terminalFailure(stream, req, fmt.Errorf("awsec2/tag: %w", err))
	}
	outputs, err := json.Marshal(map[string]any{"instanceId": p.InstanceID, "tagged": len(tags)})
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("awsec2/tag: marshal outputs: %w", err))
	}
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level:         pluginv1.TaskEvent_LEVEL_INFO,
			Message:       fmt.Sprintf("tagged %s", p.InstanceID),
			At:            timestamppb.Now(),
			CorrelationId: req.GetEnvelope().GetCorrelationId(),
			Fields:        map[string]string{"instanceId": p.InstanceID},
			Terminal:      true,
			Ok:            true,
		},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: outputs},
			OutputContract: &pluginv1.ContractRef{SchemaId: "actions/awsec2/tag.output"},
		},
	})
}

// terminalDryRun emits a terminal ok event for a dry-run that passed the EC2 permission
// check (no side effect happened): the plan succeeded, so the Result carries the output
// contract but no bindable outputs and no Entity.
func (s *Server) terminalDryRun(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, msg, outputContract string) error {
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level:         pluginv1.TaskEvent_LEVEL_INFO,
			Message:       msg,
			At:            timestamppb.Now(),
			CorrelationId: req.GetEnvelope().GetCorrelationId(),
			Terminal:      true,
			Ok:            true,
		},
		Result: &pluginv1.InvokeResult{OutputContract: &pluginv1.ContractRef{SchemaId: outputContract}},
	})
}

// terminalFailure emits the terminal, not-ok TaskEvent (no Result) and returns nil
// — a domain failure rides the typed descent channel, it is not a transport error.
func (s *Server) terminalFailure(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, cause error) error {
	s.log.Error("action failed", "error", cause)
	return stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level:         pluginv1.TaskEvent_LEVEL_ERROR,
		Message:       cause.Error(),
		At:            timestamppb.Now(),
		CorrelationId: req.GetEnvelope().GetCorrelationId(),
		Terminal:      true,
		Ok:            false,
	}})
}

// isDryRunSuccess reports whether err is EC2's DryRunOperation signal — the
// permission check passed and no instance was created.
func isDryRunSuccess(err error) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && ae.ErrorCode() == "DryRunOperation"
}
