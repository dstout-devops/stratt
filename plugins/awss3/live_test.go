package awss3

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// TestLiveBucketLifecycleAgainstSeaweedFS exercises the awss3 Connector against a real
// S3-compatible backend (SeaweedFS, ADR-0093/0097) through the plugin's port: create a
// bucket, Observe it as a metadata-only Entity, best-effort enable versioning, then
// delete. Gated on STRATT_LIVE_S3_ENDPOINT. Run with:
//
//	STRATT_LIVE_S3_ENDPOINT=http://localhost:8333 AWS_ACCESS_KEY_ID=any \
//	  AWS_SECRET_ACCESS_KEY=any AWS_REGION=us-east-1 go test ./ -run LiveBucket -v
func TestLiveBucketLifecycleAgainstSeaweedFS(t *testing.T) {
	endpoint := os.Getenv("STRATT_LIVE_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set STRATT_LIVE_S3_ENDPOINT (the SeaweedFS S3 endpoint) to run the live bucket proof")
	}
	srv := NewServer(Config{Region: "us-east-1", Endpoint: endpoint, PathStyle: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	const bucket = "stratt-d-live"

	run := func(action string, args any) (*pluginv1.InvokeResult, bool) {
		raw, _ := json.Marshal(args)
		stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
		if err := srv.Invoke(&pluginv1.InvokeRequest{Action: action, Args: &pluginv1.Payload{Bytes: raw}}, stream); err != nil {
			t.Fatalf("%s transport: %v", action, err)
		}
		term := stream.sent[len(stream.sent)-1]
		return term.GetResult(), term.GetEvent().GetOk()
	}

	// Best-effort cleanup of a prior run, then create.
	run("awss3/delete-bucket", map[string]any{"name": bucket})
	res, ok := run("awss3/create-bucket", map[string]any{"name": bucket})
	if !ok {
		t.Fatal("create-bucket failed against SeaweedFS")
	}
	var created map[string]any
	_ = json.Unmarshal(res.GetOutputs().GetBytes(), &created)
	t.Logf("LIVE created bucket arn=%v", created["bucketArn"])
	defer run("awss3/delete-bucket", map[string]any{"name": bucket}) // cleanup

	// Observe — the bucket must project as a metadata-only Entity.
	ostream := &captureStream[pluginv1.ObserveResponse]{ctx: context.Background()}
	if err := srv.Observe(&pluginv1.ObserveRequest{}, ostream); err != nil {
		t.Fatalf("observe: %v", err)
	}
	found := false
	for _, resp := range ostream.sent {
		for _, e := range resp.GetEntities() {
			if e.GetIdentityKeys()["aws.bucketArn"] == "arn:aws:s3:::"+bucket {
				found = true
				if e.GetKind() != "bucket" {
					t.Errorf("kind=%q, want bucket", e.GetKind())
				}
			}
		}
	}
	if !found {
		t.Fatalf("Observe did not project the created bucket %s", bucket)
	}
	t.Logf("LIVE observed bucket %s as a metadata-only Entity", bucket)

	// Best-effort versioning (SeaweedFS may not support it — tolerated, §1.8 Warn-logged).
	if _, vok := run("awss3/enable-versioning", map[string]any{"name": bucket}); vok {
		t.Logf("LIVE enable-versioning ok")
	} else {
		t.Logf("LIVE enable-versioning not supported by backend (tolerated)")
	}
}
