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
	actionCreateBucket     = "awss3/create-bucket"
	actionDeleteBucket     = "awss3/delete-bucket"
	actionEnableVersioning = "awss3/enable-versioning"
	actionPutPolicy        = "awss3/put-bucket-policy"
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
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:         s.cfg.PluginID,
		ProtocolVersion:  "v1",
		Class:            pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:            []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE, pluginv1.Verb_VERB_INVOKE},
		Contracts:        contracts,
		TombstoneSchemes: []string{"aws.bucketArn"},
		Actions: []*pluginv1.ActionDecl{
			decl(actionCreateBucket, false),
			decl(actionDeleteBucket, false),
			decl(actionEnableVersioning, true),
			decl(actionPutPolicy, true),
		},
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

// terminalOK emits the terminal ok event with typed outputs + the output contract. The
// bucket Entity itself arrives from the Syncer's next poll (§1.2), not a Run-provenance
// write here — this Action mutates config the Syncer owns projecting.
func (s *Server) terminalOK(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, op, name string, outputs map[string]any) error {
	raw, err := json.Marshal(outputs)
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("awss3/%s: marshal outputs: %w", op, err))
	}
	s.log.Info("bucket action ok", "op", op, "bucket", name)
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level: pluginv1.TaskEvent_LEVEL_INFO, Message: fmt.Sprintf("%s %s ok", op, name),
			At: nowpb(), CorrelationId: req.GetEnvelope().GetCorrelationId(), Terminal: true, Ok: true,
		},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: raw},
			OutputContract: &pluginv1.ContractRef{SchemaId: "actions/awss3/" + op + ".output"},
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
