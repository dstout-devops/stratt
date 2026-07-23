// Package evidencestore seals Finding Evidence bundles into an S3-compatible
// object store (charter §2.4: "Evidence — immutable (object-locked) artifact
// bundle backing a Finding; the audit/PCI export unit"; ADR-0029).
//
// Immutability is defense-in-depth:
//   - **object-lock retention** is set on every sealed object (RetainUntilDate);
//     a compliant backend (AWS S3 Object Lock) enforces WORM at the storage
//     layer. NOTE: the dev SeaweedFS stores the lock config but does NOT enforce
//     it (verified empirically, ADR-0029) — so in dev the guarantee below carries
//     the weight.
//   - **sha256 tamper-evidence** (backend-independent): the content hash is
//     recorded in the graph manifest; every read re-hashes and rejects a
//     mismatch, so any post-seal mutation is detected regardless of backend.
//   - **write-once by construction**: the platform never overwrites or deletes a
//     sealed object; re-sealing an already-sealed Finding is a no-op upstream.
//
// Credentials arrive via the SDK's standard environment chain (§2.5 env-stub,
// never persisted), reusing the moto/AWS wiring the EC2 Syncer uses.
package evidencestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/dstout-devops/stratt/core/internal/objectstore"
)

// Store seals and retrieves Evidence objects. It layers WORM object-lock +
// sha256 tamper-evidence over a shared object-store client (objectstore).
type Store struct {
	cl     *s3.Client
	bucket string
	retain time.Duration
}

// Sealed is the result of a Seal — the manifest the graph records.
type Sealed struct {
	Key         string
	SHA256      string
	Size        int64
	RetainUntil time.Time
}

// New builds a Store over a shared object-store Client (objectstore.New). bucket is
// the evidence WORM bucket; retentionDays sets the object-lock retain window applied
// at seal time (≤0 ⇒ 365). It does NOT touch the network; call EnsureBucket to provision.
func New(cl *objectstore.Client, bucket string, retentionDays int) (*Store, error) {
	if bucket == "" {
		return nil, fmt.Errorf("evidencestore: bucket is required")
	}
	if cl == nil || cl.S3 == nil {
		return nil, fmt.Errorf("evidencestore: object-store client is required")
	}
	days := retentionDays
	if days <= 0 {
		days = 365
	}
	return &Store{cl: cl.S3, bucket: bucket, retain: time.Duration(days) * 24 * time.Hour}, nil
}

// EnsureBucket creates the bucket with Object Lock enabled if absent
// (idempotent). Object Lock must be enabled at creation and implies versioning.
func (s *Store) EnsureBucket(ctx context.Context) error {
	_, err := s.cl.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket:                     aws.String(s.bucket),
		ObjectLockEnabledForBucket: aws.Bool(true),
	})
	if err != nil && !isAlreadyOwned(err) {
		return fmt.Errorf("evidencestore: ensure bucket %s: %w", s.bucket, err)
	}
	return nil
}

// Seal writes body under key with an object-lock retention window and returns
// the manifest. The content sha256 is the tamper-evidence anchor. Seal is
// write-once by contract: callers must not re-seal an existing key.
func (s *Store) Seal(ctx context.Context, key string, body []byte) (Sealed, error) {
	sum := sha256.Sum256(body)
	hexSum := hex.EncodeToString(sum[:])
	retainUntil := time.Now().UTC().Add(s.retain)
	_, err := s.cl.PutObject(ctx, &s3.PutObjectInput{
		Bucket:                    aws.String(s.bucket),
		Key:                       aws.String(key),
		Body:                      bytes.NewReader(body),
		ContentType:               aws.String("application/json"),
		ObjectLockMode:            s3types.ObjectLockModeCompliance,
		ObjectLockRetainUntilDate: aws.Time(retainUntil),
		// The hash rides as object metadata too, so the object is
		// self-describing even without the graph manifest.
		Metadata: map[string]string{"sha256": hexSum},
	})
	if err != nil {
		return Sealed{}, fmt.Errorf("evidencestore: seal %s: %w", key, err)
	}
	return Sealed{Key: key, SHA256: hexSum, Size: int64(len(body)), RetainUntil: retainUntil}, nil
}

// GetVerified fetches key and verifies its sha256 against wantSHA. A mismatch is
// ErrTampered — the backend-independent immutability guarantee (§1.8: a mutated
// Evidence object is DETECTED on read, never served as authentic).
func (s *Store) GetVerified(ctx context.Context, key, wantSHA string) ([]byte, error) {
	out, err := s.cl.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("evidencestore: get %s: %w", key, err)
	}
	defer out.Body.Close()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("evidencestore: read %s: %w", key, err)
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		return nil, fmt.Errorf("%w: key %s sha256=%s want=%s", ErrTampered, key, got, wantSHA)
	}
	return body, nil
}

// ErrTampered signals a sealed Evidence object whose content no longer matches
// its recorded hash.
var ErrTampered = fmt.Errorf("evidencestore: evidence object failed integrity check")

func isAlreadyOwned(err error) bool {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		c := ae.ErrorCode()
		return c == "BucketAlreadyOwnedByYou" || c == "BucketAlreadyExists"
	}
	return false
}
