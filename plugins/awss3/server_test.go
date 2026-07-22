package awss3

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// fakeS3 injects the S3 control API — the plugin's content-expertise in isolation, no
// network (the ADR-0046 module-isolation point). Object-plane methods are absent by
// design (§1.2). errBy names an operation to fail (to simulate an unsupported backend).
type fakeS3 struct {
	buckets    []s3types.Bucket
	versioning s3types.BucketVersioningStatus
	tagged     bool // whether GetBucketTagging returns a stratt:managed tag
	unsupport  map[string]bool
	lastCreate *s3.CreateBucketInput
	lastDelete *s3.DeleteBucketInput
	lastVer    *s3.PutBucketVersioningInput
	lastPolicy *s3.PutBucketPolicyInput
	lastTag    *s3.PutBucketTaggingInput
}

func (f *fakeS3) fail(op string) error {
	if f.unsupport[op] {
		return &s3types.NotFound{}
	}
	return nil
}
func (f *fakeS3) ListBuckets(context.Context, *s3.ListBucketsInput, ...func(*s3.Options)) (*s3.ListBucketsOutput, error) {
	return &s3.ListBucketsOutput{Buckets: f.buckets}, nil
}
func (f *fakeS3) CreateBucket(_ context.Context, in *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	f.lastCreate = in
	return &s3.CreateBucketOutput{}, f.fail("CreateBucket")
}
func (f *fakeS3) DeleteBucket(_ context.Context, in *s3.DeleteBucketInput, _ ...func(*s3.Options)) (*s3.DeleteBucketOutput, error) {
	f.lastDelete = in
	return &s3.DeleteBucketOutput{}, f.fail("DeleteBucket")
}
func (f *fakeS3) PutBucketVersioning(_ context.Context, in *s3.PutBucketVersioningInput, _ ...func(*s3.Options)) (*s3.PutBucketVersioningOutput, error) {
	f.lastVer = in
	return &s3.PutBucketVersioningOutput{}, f.fail("PutBucketVersioning")
}
func (f *fakeS3) GetBucketVersioning(_ context.Context, _ *s3.GetBucketVersioningInput, _ ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error) {
	if err := f.fail("GetBucketVersioning"); err != nil {
		return nil, err
	}
	return &s3.GetBucketVersioningOutput{Status: f.versioning}, nil
}
func (f *fakeS3) PutBucketPolicy(_ context.Context, in *s3.PutBucketPolicyInput, _ ...func(*s3.Options)) (*s3.PutBucketPolicyOutput, error) {
	f.lastPolicy = in
	return &s3.PutBucketPolicyOutput{}, f.fail("PutBucketPolicy")
}
func (f *fakeS3) PutBucketTagging(_ context.Context, in *s3.PutBucketTaggingInput, _ ...func(*s3.Options)) (*s3.PutBucketTaggingOutput, error) {
	f.lastTag = in
	return &s3.PutBucketTaggingOutput{}, f.fail("PutBucketTagging")
}
func (f *fakeS3) GetBucketTagging(_ context.Context, _ *s3.GetBucketTaggingInput, _ ...func(*s3.Options)) (*s3.GetBucketTaggingOutput, error) {
	if err := f.fail("GetBucketTagging"); err != nil {
		return nil, err
	}
	if f.tagged {
		return &s3.GetBucketTaggingOutput{TagSet: []s3types.Tag{{Key: aws.String("stratt:managed"), Value: aws.String("true")}}}, nil
	}
	return &s3.GetBucketTaggingOutput{}, nil
}

type captureStream[T any] struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*T
}

func (s *captureStream[T]) Context() context.Context { return s.ctx }
func (s *captureStream[T]) Send(m *T) error          { s.sent = append(s.sent, m); return nil }

func newServer(t *testing.T, api S3API, protected ...string) *Server {
	t.Helper()
	s := NewServer(Config{Region: "us-east-1", ProtectedBuckets: protected}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.newAPI = func(context.Context) (S3API, error) { return api, nil }
	return s
}

// TestObserveEmitsBuckets proves the metadata-only Syncer: ListBuckets → bucket Entities
// (identity aws.bucketArn, bucket.config facet), versioning enrichment, stratt.managed
// label from the marker tag. It never lists objects.
func TestObserveEmitsBuckets(t *testing.T) {
	created := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	api := &fakeS3{
		buckets:    []s3types.Bucket{{Name: aws.String("data-lake"), CreationDate: &created}},
		versioning: s3types.BucketVersioningStatusEnabled,
		tagged:     true,
	}
	stream := &captureStream[pluginv1.ObserveResponse]{ctx: context.Background()}
	if err := newServer(t, api).Observe(&pluginv1.ObserveRequest{}, stream); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if len(stream.sent) != 1 || !stream.sent[0].GetFullSyncComplete() {
		t.Fatal("expected one full-sync ObserveResponse")
	}
	ents := stream.sent[0].GetEntities()
	if len(ents) != 1 {
		t.Fatalf("expected one bucket, got %d", len(ents))
	}
	e := ents[0]
	if e.GetKind() != "bucket" || e.GetIdentityKeys()["aws.bucketArn"] != "arn:aws:s3:::data-lake" {
		t.Fatalf("bucket identity: kind=%q id=%v", e.GetKind(), e.GetIdentityKeys())
	}
	if e.GetLabels()["stratt.managed"] != "true" {
		t.Errorf("marker tag must surface as stratt.managed label: %v", e.GetLabels())
	}
	var cfg map[string]any
	_ = json.Unmarshal(e.GetFacets()["bucket.config"], &cfg)
	if cfg["versioning"] != "Enabled" {
		t.Errorf("bucket.config.versioning: %v", cfg)
	}
}

// TestObserveToleratesUnsupportedEnrichment — a backend lacking versioning/tagging still
// syncs (SeaweedFS): the bucket is projected without those signals, never a failure.
func TestObserveToleratesUnsupportedEnrichment(t *testing.T) {
	api := &fakeS3{
		buckets:   []s3types.Bucket{{Name: aws.String("b1")}},
		unsupport: map[string]bool{"GetBucketVersioning": true, "GetBucketTagging": true},
	}
	stream := &captureStream[pluginv1.ObserveResponse]{ctx: context.Background()}
	if err := newServer(t, api).Observe(&pluginv1.ObserveRequest{}, stream); err != nil {
		t.Fatalf("observe must tolerate unsupported enrichment: %v", err)
	}
	e := stream.sent[0].GetEntities()[0]
	if _, ok := e.GetLabels()["stratt.managed"]; ok {
		t.Error("no tag read ⇒ no stratt.managed label")
	}
}

func TestInvokeBucketActions(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		api := &fakeS3{}
		res := invoke(t, newServer(t, api), "awss3/create-bucket", map[string]any{"name": "new-bucket"})
		if outMap(t, res)["bucketArn"] != "arn:aws:s3:::new-bucket" {
			t.Fatalf("bucketArn: %v", outMap(t, res))
		}
		if aws.ToString(api.lastCreate.Bucket) != "new-bucket" {
			t.Errorf("CreateBucket name: %v", api.lastCreate)
		}
		// Anti-orphan marker attempted.
		if api.lastTag == nil {
			t.Error("create-bucket must attempt the stratt:managed tag")
		}
	})
	t.Run("delete", func(t *testing.T) {
		api := &fakeS3{}
		invoke(t, newServer(t, api), "awss3/delete-bucket", map[string]any{"name": "old"})
		if aws.ToString(api.lastDelete.Bucket) != "old" {
			t.Errorf("DeleteBucket: %v", api.lastDelete)
		}
	})
	t.Run("enable-versioning", func(t *testing.T) {
		api := &fakeS3{}
		res := invoke(t, newServer(t, api), "awss3/enable-versioning", map[string]any{"name": "b"})
		if outMap(t, res)["versioning"] != "Enabled" || api.lastVer.VersioningConfiguration.Status != s3types.BucketVersioningStatusEnabled {
			t.Errorf("versioning not enabled: %v", api.lastVer)
		}
	})
	t.Run("put-policy", func(t *testing.T) {
		api := &fakeS3{}
		invoke(t, newServer(t, api), "awss3/put-bucket-policy", map[string]any{"name": "b", "policy": `{"Version":"2012-10-17"}`})
		if aws.ToString(api.lastPolicy.Policy) != `{"Version":"2012-10-17"}` {
			t.Errorf("policy not passed opaque: %v", api.lastPolicy)
		}
	})
}

// TestProtectedBucketRefused proves the Evidence-WORM guard (ADR-0097 flag 2): delete +
// put-policy on a protected bucket are refused; create/observe are unaffected.
func TestProtectedBucketRefused(t *testing.T) {
	for _, action := range []string{"awss3/delete-bucket", "awss3/put-bucket-policy"} {
		api := &fakeS3{}
		srv := newServer(t, api, "stratt-evidence")
		args, _ := json.Marshal(map[string]any{"name": "stratt-evidence", "policy": "{}"})
		stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
		err := srv.Invoke(&pluginv1.InvokeRequest{Action: action, Args: &pluginv1.Payload{Bytes: args}}, stream)
		if err == nil {
			t.Fatalf("%s on a protected bucket must be refused", action)
		}
		if api.lastDelete != nil || api.lastPolicy != nil {
			t.Fatalf("%s must NOT reach the S3 API for a protected bucket", action)
		}
	}
}

func TestGetManifest(t *testing.T) {
	m, err := newServer(t, &fakeS3{}).GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	man := m.GetManifest()
	if man.GetClass() != pluginv1.PluginClass_PLUGIN_CLASS_SYNCER {
		t.Errorf("class: %v", man.GetClass())
	}
	if len(man.GetContracts()) != 1 || man.GetContracts()[0].GetSchemaId() != "bucket.config" {
		t.Errorf("contracts: %v", man.GetContracts())
	}
	if len(man.GetTombstoneSchemes()) != 1 || man.GetTombstoneSchemes()[0] != "aws.bucketArn" {
		t.Errorf("tombstone: %v", man.GetTombstoneSchemes())
	}
	names := map[string]bool{}
	for _, a := range man.GetActions() {
		names[a.GetName()] = true
	}
	for _, want := range []string{"awss3/create-bucket", "awss3/delete-bucket", "awss3/enable-versioning", "awss3/put-bucket-policy"} {
		if !names[want] {
			t.Errorf("missing action %q", want)
		}
	}
}

// TestNormalizedBucketFacetMatchesContract is the co-fidelity guard (ADR-0095 flag-2
// discipline): the emitted bucket.config keys are a subset of the closed schema.
func TestNormalizedBucketFacetMatchesContract(t *testing.T) {
	created := time.Now().UTC()
	e := normalizeBucket("us-east-1", "b", &created, "Enabled", true)
	raw := e.GetFacets()["bucket.config"]
	if len(raw) == 0 {
		t.Fatal("expected a bucket.config facet")
	}
	allowed := schemaProps(t, "bucket.config")
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	for k := range doc {
		if !allowed[k] {
			t.Errorf("bucket.config emits key %q not in its CLOSED schema — the live Syncer would REJECT this write", k)
		}
	}
}

func schemaProps(t *testing.T, ns string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "facets", ns+".schema.json"))
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var doc struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	_ = json.Unmarshal(raw, &doc)
	if doc.AdditionalProperties == nil || *doc.AdditionalProperties {
		t.Fatalf("%s must be CLOSED", ns)
	}
	out := map[string]bool{}
	for k := range doc.Properties {
		out[k] = true
	}
	return out
}

// invoke runs one Action and returns the terminal result (fatal on not-ok).
func invoke(t *testing.T, srv *Server, action string, args any) *pluginv1.InvokeResult {
	t.Helper()
	raw, _ := json.Marshal(args)
	stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
	if err := srv.Invoke(&pluginv1.InvokeRequest{Action: action, Args: &pluginv1.Payload{Bytes: raw}}, stream); err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	term := stream.sent[len(stream.sent)-1]
	if !term.GetEvent().GetTerminal() || !term.GetEvent().GetOk() {
		t.Fatalf("%s not terminal-ok: %q", action, term.GetEvent().GetMessage())
	}
	return term.GetResult()
}

func outMap(t *testing.T, res *pluginv1.InvokeResult) map[string]any {
	t.Helper()
	var m map[string]any
	_ = json.Unmarshal(res.GetOutputs().GetBytes(), &m)
	return m
}
