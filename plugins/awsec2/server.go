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

// actionCreateVM is the sole Action this plugin advertises (ActionDecl.name); the
// InvokeRequest.action selector picks it. "" is accepted as the sole Action.
const actionCreateVM = "awsec2/create-vm"

// Facet namespaces this Syncer REQUESTS to own (§2.1); the core honors them only
// where the operator grant allows.
var facetNamespaces = []string{
	"instance.compute", // instanceType, architecture, imageId
	"instance.network", // ips, vpc, subnet, az
	"instance.state",   // lifecycle state, launch time
}

// EC2API is the small slice of the EC2 API this plugin needs — DescribeInstances
// for the Syncer, RunInstances for the create-vm Action. Abstracting it behind an
// interface lets tests inject fakes with no AWS calls (the ADR-0046 module-isolation
// proof: the plugin's content-expertise is exercised in isolation). *ec2.Client
// satisfies it; so does ec2.DescribeInstancesAPIClient for the paginator.
type EC2API interface {
	DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	RunInstances(ctx context.Context, in *ec2.RunInstancesInput, opts ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
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
		TombstoneSchemes: []string{"aws.instanceId"},
		Actions: []*pluginv1.ActionDecl{{
			Name:        actionCreateVM,
			Input:       &pluginv1.ContractRef{SchemaId: "actions/awsec2/create-vm.input"},
			Output:      &pluginv1.ContractRef{SchemaId: "actions/awsec2/create-vm.output"},
			Idempotent:  false, // each call provisions a new instance
			DryRunnable: true,  // RunInstances supports DryRun
		}},
	}}, nil
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

// observe paginates DescribeInstances and normalizes each live instance. Pure
// content-expertise; no graph writes (the plugin holds no DB path).
func observe(ctx context.Context, api EC2API, region string) ([]*pluginv1.ObservedEntity, error) {
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
	if action := req.GetAction(); action != "" && action != actionCreateVM {
		return status.Errorf(codes.InvalidArgument, "awsec2: unknown action %q", action)
	}

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

	api, err := s.newAPI(ctx)
	if err != nil {
		return err
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

// terminalFailure emits the terminal, not-ok TaskEvent (no Result) and returns nil
// — a domain failure rides the typed descent channel, it is not a transport error.
func (s *Server) terminalFailure(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, cause error) error {
	s.log.Error("create-vm failed", "error", cause)
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
