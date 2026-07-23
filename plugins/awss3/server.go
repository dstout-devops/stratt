// Package awss3 is the AWS S3 Connector plugin (ADR-0097): a metadata-only bucket
// Syncer + bucket lifecycle Actions over the sovereign plugin port (ADR-0046). It maps
// S3 bucket METADATA to core-legible ObservedEntity values — existence, region,
// versioning, creation date — and NEVER object contents (§1.2: the graph is estate
// topology, not a copy of the data plane). The plugin holds no graph write path.
package awss3

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// nowpb is the current time as a proto timestamp (plugin code — time.Now is fine here).
func nowpb() *timestamppb.Timestamp { return timestamppb.Now() }

// Action names this plugin advertises.
const (
	actionCreateBucket      = "awss3/create-bucket"
	actionDeleteBucket      = "awss3/delete-bucket"
	actionEnableVersioning  = "awss3/enable-versioning"
	actionPutPolicy         = "awss3/put-bucket-policy"
	actionStatestoreResolve = "awss3/statestore-resolve" // the statestore capability's resolve Action (ADR-0105)
)

// facetNamespaces this Syncer REQUESTS to own (§2.1) — metadata only.
var facetNamespaces = []string{"bucket.config"}

// S3API is the slice of the S3 control API this plugin needs. Object-plane calls
// (Get/Put/ListObjects) are DELIBERATELY absent — the Syncer never touches object bytes
// (§1.2). *s3.Client satisfies it; tests inject a fake.
type S3API interface {
	ListBuckets(ctx context.Context, in *s3.ListBucketsInput, opts ...func(*s3.Options)) (*s3.ListBucketsOutput, error)
	CreateBucket(ctx context.Context, in *s3.CreateBucketInput, opts ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
	DeleteBucket(ctx context.Context, in *s3.DeleteBucketInput, opts ...func(*s3.Options)) (*s3.DeleteBucketOutput, error)
	PutBucketVersioning(ctx context.Context, in *s3.PutBucketVersioningInput, opts ...func(*s3.Options)) (*s3.PutBucketVersioningOutput, error)
	GetBucketVersioning(ctx context.Context, in *s3.GetBucketVersioningInput, opts ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error)
	PutBucketPolicy(ctx context.Context, in *s3.PutBucketPolicyInput, opts ...func(*s3.Options)) (*s3.PutBucketPolicyOutput, error)
	PutBucketTagging(ctx context.Context, in *s3.PutBucketTaggingInput, opts ...func(*s3.Options)) (*s3.PutBucketTaggingOutput, error)
	GetBucketTagging(ctx context.Context, in *s3.GetBucketTaggingInput, opts ...func(*s3.Options)) (*s3.GetBucketTaggingOutput, error)
}

// Config locates the S3 Source. Credentials arrive resolved from the SDK chain at spawn
// (AWS_ACCESS_KEY_ID etc.); material never crosses the core (§2.5).
type Config struct {
	PluginID  string
	Endpoint  string // API endpoint override (dev: SeaweedFS on :8333)
	Region    string
	PathStyle bool // path-style addressing (required for SeaweedFS / most S3-compatibles)
	// ProtectedBuckets are buckets the destructive Actions (delete-bucket,
	// put-bucket-policy) REFUSE (ADR-0097 guardian flag 2) — notably the Evidence
	// store's WORM bucket (ADR-0029), which SeaweedFS does not protect in dev. So the
	// connector can never become the hole in the Evidence store's write-once story.
	ProtectedBuckets []string
	// StateBucket, when set, makes this plugin a `statestore` capability provider (ADR-0105):
	// the awss3/statestore-resolve Action returns an s3 tofu-backend config keyed into this
	// bucket. Empty ⇒ the plugin does not provide statestore.
	StateBucket string
	// StateCredentialRef is the §2.5 CredentialRef NAME for the state bucket's auth material
	// (resolved at the consumer pod's spawn, never inline). Empty ⇒ the consumer's SDK env chain.
	StateCredentialRef string
}

// isProtected reports whether a bucket is on the refuse-list for destructive Actions.
func (s *Server) isProtected(name string) bool {
	for _, p := range s.cfg.ProtectedBuckets {
		if p != "" && p == name {
			return true
		}
	}
	return false
}

// Server implements the sovereign plugin port for the awss3 Connector — a SYNCER-class
// plugin advertising OBSERVE (the bucket Syncer) AND INVOKE (bucket lifecycle Actions).
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg    Config
	log    *slog.Logger
	newAPI func(context.Context) (S3API, error)
}

func NewServer(cfg Config, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "awss3"
	}
	s := &Server{cfg: cfg, log: log.With("plugin", "awss3")}
	s.newAPI = s.buildClient
	return s
}

// buildClient mirrors evidencestore.New: the standard config chain + endpoint/path-style
// overrides for an S3-compatible backend.
func (s *Server) buildClient(ctx context.Context) (S3API, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(s.cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("awss3: load config: %w", err)
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if s.cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(s.cfg.Endpoint)
		}
		o.UsePathStyle = s.cfg.PathStyle
	}), nil
}

func (s *Server) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	return &pluginv1.HealthResponse{Status: pluginv1.HealthResponse_SERVING_UP}, nil
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	contracts := make([]*pluginv1.ContractDecl, 0, len(facetNamespaces))
	for _, ns := range facetNamespaces {
		contracts = append(contracts, &pluginv1.ContractDecl{SchemaId: ns})
	}
	decl := func(name string, idempotent bool) *pluginv1.ActionDecl {
		op := name[len("awss3/"):]
		return &pluginv1.ActionDecl{
			Name:   name,
			Input:  &pluginv1.ContractRef{SchemaId: "actions/awss3/" + op + ".input"},
			Output: &pluginv1.ContractRef{SchemaId: "actions/awss3/" + op + ".output"},
			// create/delete are not idempotent (exists/absent errors); versioning +
			// policy are idempotent (set-to-a-value). All non-DryRunnable (S3 has no
			// dry-run operation).
			Idempotent: idempotent,
		}
	}
	actions := []*pluginv1.ActionDecl{
		decl(actionCreateBucket, false),
		decl(actionDeleteBucket, false),
		decl(actionEnableVersioning, true),
		decl(actionPutPolicy, true),
	}
	// statestore capability (ADR-0105): advertised ONLY when a state bucket is configured, so
	// provider verification (ADR-0104 D1) confirms the plugin genuinely backs what it declares.
	// Its resolve Action references the CLASS-level, provider-agnostic Contract (not a plugin-
	// scoped one), so every statestore provider conforms to one shape (D3).
	var capabilities []string
	if s.cfg.StateBucket != "" {
		capabilities = append(capabilities, "statestore")
		actions = append(actions, &pluginv1.ActionDecl{
			Name:       actionStatestoreResolve,
			Input:      &pluginv1.ContractRef{SchemaId: "capabilities/statestore.input"},
			Output:     &pluginv1.ContractRef{SchemaId: "capabilities/statestore.output"},
			Idempotent: true, // resolution is a pure read of config — no side effects
		})
	}
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:         s.cfg.PluginID,
		ProtocolVersion:  "v1",
		Class:            pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:            []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE, pluginv1.Verb_VERB_INVOKE},
		Contracts:        contracts,
		TombstoneSchemes: []string{"aws.bucketArn"},
		Capabilities:     capabilities,
		Actions:          actions,
	}}, nil
}

// Observe performs a full sync of bucket METADATA: ListBuckets → one bucket Entity each,
// best-effort enriched with versioning + the stratt.managed marker. NEVER lists or reads
// objects (§1.2). One ObserveResponse with the full_sync_complete tombstone boundary.
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	ctx := stream.Context()
	api, err := s.newAPI(ctx)
	if err != nil {
		return err
	}
	entities, err := s.observe(ctx, api)
	if err != nil {
		return err
	}
	s.log.Info("full sync", "buckets", len(entities))
	return stream.Send(&pluginv1.ObserveResponse{Entities: entities, FullSyncComplete: true})
}

// observe lists buckets and normalizes each with best-effort metadata enrichment. The
// enrichment calls (GetBucketVersioning/GetBucketTagging) are tolerant of backends that
// lack them (SeaweedFS) — a failure just omits that signal, never fails the sync. When
// an enrichment is unavailable it is reported ONCE per sync (§1.8: diagnosed, not
// silent; once, not per-bucket spam).
func (s *Server) observe(ctx context.Context, api S3API) ([]*pluginv1.ObservedEntity, error) {
	out, err := api.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, fmt.Errorf("awss3: list buckets: %w", err)
	}
	var entities []*pluginv1.ObservedEntity
	verUnavailable, tagUnavailable := false, false
	for _, b := range out.Buckets {
		name := aws.ToString(b.Name)
		if name == "" {
			continue
		}
		versioning := ""
		if v, verr := api.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: b.Name}); verr == nil {
			versioning = string(v.Status)
		} else {
			verUnavailable = true
		}
		managed := false
		if t, terr := api.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: b.Name}); terr == nil {
			for _, tag := range t.TagSet {
				if aws.ToString(tag.Key) == "stratt:managed" && aws.ToString(tag.Value) == "true" {
					managed = true
				}
			}
		} else {
			tagUnavailable = true
		}
		entities = append(entities, normalizeBucket(s.cfg.Region, name, b.CreationDate, versioning, managed))
	}
	if verUnavailable {
		s.log.Warn("bucket versioning enrichment unavailable on this backend — bucket.config omits versioning")
	}
	if tagUnavailable {
		s.log.Warn("bucket tagging read unavailable on this backend — the stratt.managed label cannot be derived")
	}
	return entities, nil
}

// bucketParams is the shared input shape (name + optional region) for the bucket Actions.
type bucketParams struct {
	Name   string `json:"name"`
	Region string `json:"region"`
	Policy string `json:"policy"` // put-bucket-policy only: opaque JSON policy document
}

// Invoke dispatches the bucket lifecycle Actions. Content-blind: an unshipped action is
// rejected, never guessed.
func (s *Server) Invoke(req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	ctx := stream.Context()
	// statestore/resolve is workspace-scoped (not bucket-scoped) and touches no S3 API — a pure
	// config read (ADR-0105). Handle it before the bucket-Action path.
	if req.GetAction() == actionStatestoreResolve {
		return s.resolveStatestore(stream, req)
	}
	api, err := s.newAPI(ctx)
	if err != nil {
		return err
	}
	action := req.GetAction()
	var p bucketParams
	if args := req.GetArgs(); args != nil && len(args.GetBytes()) > 0 {
		if err := json.Unmarshal(args.GetBytes(), &p); err != nil {
			return status.Errorf(codes.InvalidArgument, "%s: invalid args: %v", action, err)
		}
	}
	if p.Name == "" {
		return status.Errorf(codes.InvalidArgument, "%s requires name", action)
	}
	// Guard the Evidence WORM bucket (and any operator-protected bucket) against the
	// destructive Actions — refuse LOUDLY (§1.8, ADR-0029 integrity), never silently.
	if (action == actionDeleteBucket || action == actionPutPolicy) && s.isProtected(p.Name) {
		return status.Errorf(codes.PermissionDenied, "%s: bucket %q is protected (e.g. the Evidence WORM store, ADR-0029) — refused", action, p.Name)
	}
	op := action[len("awss3/"):]
	if err := s.progress(stream, req, fmt.Sprintf("%s %s", op, p.Name)); err != nil {
		return err
	}

	var outputs map[string]any
	switch action {
	case actionCreateBucket:
		in := &s3.CreateBucketInput{Bucket: aws.String(p.Name)}
		if p.Region != "" && p.Region != "us-east-1" {
			in.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
				LocationConstraint: s3types.BucketLocationConstraint(p.Region),
			}
		}
		if _, err := api.CreateBucket(ctx, in); err != nil {
			return s.terminalFailure(stream, req, fmt.Errorf("%s: %w", action, err))
		}
		// Best-effort anti-orphan marker (ADR-0095 flag 1) — tolerated if the backend
		// lacks tagging (SeaweedFS); the bucket exists regardless.
		if _, terr := api.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
			Bucket:  aws.String(p.Name),
			Tagging: &s3types.Tagging{TagSet: []s3types.Tag{{Key: aws.String("stratt:managed"), Value: aws.String("true")}}},
		}); terr != nil {
			// §1.8 (guardian flag 1): a dropped managed-stamp is a diagnosis gap — a
			// Stratt-created bucket that the Syncer won't label managed. Warn, loud.
			s.log.Warn("bucket tagging unsupported — stratt:managed marker NOT stamped; this bucket will not be labelled managed by the Syncer", "bucket", p.Name, "err", terr)
		}
		outputs = map[string]any{"bucketArn": bucketArn(p.Name)}
	case actionDeleteBucket:
		if _, err := api.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(p.Name)}); err != nil {
			return s.terminalFailure(stream, req, fmt.Errorf("%s: %w", action, err))
		}
		outputs = map[string]any{"name": p.Name}
	case actionEnableVersioning:
		if _, err := api.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
			Bucket:                  aws.String(p.Name),
			VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled},
		}); err != nil {
			return s.terminalFailure(stream, req, fmt.Errorf("%s: %w", action, err))
		}
		outputs = map[string]any{"name": p.Name, "versioning": "Enabled"}
	case actionPutPolicy:
		if p.Policy == "" {
			return status.Errorf(codes.InvalidArgument, "awss3/put-bucket-policy requires policy")
		}
		if _, err := api.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{
			Bucket: aws.String(p.Name),
			Policy: aws.String(p.Policy),
		}); err != nil {
			return s.terminalFailure(stream, req, fmt.Errorf("%s: %w", action, err))
		}
		outputs = map[string]any{"name": p.Name}
	default:
		return status.Errorf(codes.InvalidArgument, "awss3: unknown action %q", action)
	}
	return s.terminalOK(stream, req, op, p.Name, outputs)
}

// progress streams one non-terminal INFO TaskEvent.
func (s *Server) progress(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, msg string) error {
	return stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, Message: msg,
		At: nowpb(), CorrelationId: req.GetEnvelope().GetCorrelationId(),
	}})
}

// resolveStatestore is the statestore capability's resolve Action (ADR-0105): given a workspace,
// it returns a PROVIDER-AGNOSTIC tofu-backend config handle (backend type + string settings + an
// optional §2.5 CredentialRef name) that the core injects into the consuming tofu Apply. It reads
// no S3 API and holds no secret material — the credential is a NAME, resolved at the consumer pod.
func (s *Server) resolveStatestore(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest) error {
	var in struct {
		Workspace string `json:"workspace"`
	}
	if args := req.GetArgs(); args != nil && len(args.GetBytes()) > 0 {
		if err := json.Unmarshal(args.GetBytes(), &in); err != nil {
			return status.Errorf(codes.InvalidArgument, "statestore-resolve: invalid args: %v", err)
		}
	}
	if in.Workspace == "" {
		return status.Errorf(codes.InvalidArgument, "statestore-resolve requires workspace")
	}
	if s.cfg.StateBucket == "" {
		return s.terminalFailure(stream, req, fmt.Errorf("statestore-resolve: no state bucket configured (STRATT_AWSS3_STATE_BUCKET)"))
	}
	region := s.cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	// The s3 tofu backend settings (emitted by the consumer as -backend-config). Native S3
	// locking (use_lockfile) — no external lock table. Endpoint/path-style for S3-compatibles.
	config := map[string]string{
		"bucket":       s.cfg.StateBucket,
		"key":          "stratt/" + in.Workspace + ".tfstate",
		"region":       region,
		"use_lockfile": "true",
	}
	if s.cfg.PathStyle {
		config["use_path_style"] = "true"
	}
	if s.cfg.Endpoint != "" {
		config["endpoints.s3"] = s.cfg.Endpoint
	}
	out := map[string]any{"backend": "s3", "config": config}
	if s.cfg.StateCredentialRef != "" {
		out["credentialRef"] = s.cfg.StateCredentialRef
	}
	s.log.Info("statestore resolved", "workspace", in.Workspace, "bucket", s.cfg.StateBucket)
	return s.sendTerminalResult(stream, req, "statestore-resolve "+in.Workspace, out, "capabilities/statestore.output")
}

// terminalOK emits the terminal ok event with typed outputs + the plugin-scoped output contract.
// The bucket Entity itself arrives from the Syncer's next poll (§1.2), not a Run-provenance write
// here — this Action mutates config the Syncer owns projecting.
func (s *Server) terminalOK(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, op, name string, outputs map[string]any) error {
	s.log.Info("bucket action ok", "op", op, "bucket", name)
	return s.sendTerminalResult(stream, req, fmt.Sprintf("%s %s ok", op, name), outputs, "actions/awss3/"+op+".output")
}

// sendTerminalResult marshals outputs and emits the terminal ok event with the given output
// Contract id (plugin-scoped for bucket Actions; the CLASS-level capabilities/… id for a
// capability resolve Action — ADR-0105 D3).
func (s *Server) sendTerminalResult(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, msg string, outputs map[string]any, contractID string) error {
	raw, err := json.Marshal(outputs)
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("marshal outputs: %w", err))
	}
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level: pluginv1.TaskEvent_LEVEL_INFO, Message: msg,
			At: nowpb(), CorrelationId: req.GetEnvelope().GetCorrelationId(), Terminal: true, Ok: true,
		},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: raw},
			OutputContract: &pluginv1.ContractRef{SchemaId: contractID},
		},
	})
}

func (s *Server) terminalFailure(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, cause error) error {
	s.log.Error("bucket action failed", "error", cause)
	return stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_ERROR, Message: cause.Error(),
		At: nowpb(), CorrelationId: req.GetEnvelope().GetCorrelationId(), Terminal: true, Ok: false,
	}})
}
