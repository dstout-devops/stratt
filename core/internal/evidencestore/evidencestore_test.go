package evidencestore

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// testStore returns a Store against the dev SeaweedFS, skipping when unreachable
// so `go test ./...` stays green pre-substrate.
func testStore(t *testing.T) *Store {
	t.Helper()
	endpoint := os.Getenv("STRATT_AWS_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:8333"
	}
	// Dev creds for the S3-compatible server (SeaweedFS accepts any).
	t.Setenv("AWS_ACCESS_KEY_ID", "dev")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "devsecret")
	ctx := context.Background()
	s, err := New(ctx, Config{Endpoint: endpoint, Region: "us-east-1", Bucket: "stratt-evidence-test", PathStyle: true, RetentionDays: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureBucket(ctx); err != nil {
		t.Skipf("no object store reachable (%v) — run `task dev:up`", err)
	}
	return s
}

// TestSealAndVerify proves the round-trip and object-lock config.
func TestSealAndVerify(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	body := []byte(`{"finding":"vm-1","immutable":true}`)

	sealed, err := s.Seal(ctx, "evidence/test-seal.json", body)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if sealed.Size != int64(len(body)) || len(sealed.SHA256) != 64 || sealed.RetainUntil.IsZero() {
		t.Fatalf("sealed manifest incomplete: %+v", sealed)
	}

	got, err := s.GetVerified(ctx, sealed.Key, sealed.SHA256)
	if err != nil {
		t.Fatalf("get verified: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("round-trip mismatch: %q", got)
	}
}

// TestTamperEvidence is the backend-independent immutability proof (§1.8,
// ADR-0029): if a sealed object is mutated out-of-band, GetVerified DETECTS it
// via the sha256 and refuses to serve it as authentic. This holds even though
// the dev SeaweedFS does not enforce object-lock WORM.
func TestTamperEvidence(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	key := "evidence/test-tamper.json"

	sealed, err := s.Seal(ctx, key, []byte(`{"finding":"original"}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	// Mutate the object directly (the dev backend permits it — that is exactly
	// the gap tamper-evidence covers).
	_, err = s.cl.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key),
		Body: strings.NewReader(`{"finding":"TAMPERED"}`),
	})
	if err != nil {
		t.Fatalf("tamper write: %v", err)
	}

	// Reading against the sealed hash must now FAIL closed.
	_, err = s.GetVerified(ctx, key, sealed.SHA256)
	if !errors.Is(err, ErrTampered) {
		t.Fatalf("mutated evidence must be detected as tampered, got %v", err)
	}
}
