// Package objectstore is the core's ONE vetted builder for an S3-compatible object
// store client (charter §2: "any S3-compatible object store — Garage / SeaweedFS /
// cloud — never a named vendor"; ADR-0002/0093). It centralizes the
// `s3.NewFromConfig` chain that `evidencestore` — and, ahead, the S3 `statestore` /
// `artifactstore` capability provider (ADR-0104/ADR-0105) — would otherwise each
// hand-copy, and it resolves the object-store connection config in ONE place so the
// endpoint/region are no longer threaded per-component (the STRATT_AWS_ENDPOINT bleed).
//
// It owns CLIENT CONSTRUCTION only. Bucket- and object-scoped concerns (evidence's
// WORM object-lock; a future statestore's per-workspace layout) layer on top of the
// returned *s3.Client. Credentials arrive via the SDK's standard env chain (§2.5
// env-stub, never persisted) — this package never reads or holds secret material.
//
// Scope note: this consolidates the CORE's object-store client. Plugins (e.g.
// plugins/awss3) are separate Go modules and cannot import core/internal, so they
// keep their own client builder — correct under §1.4 plugin isolation, not duplication
// this package can remove.
package objectstore

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Config locates the object store. Endpoint is the S3 API (dev: SeaweedFS on :8333);
// empty uses the SDK default resolver (real AWS S3). PathStyle is required for
// S3-compatible servers (SeaweedFS, Garage, moto), which don't do virtual-host buckets.
type Config struct {
	Endpoint  string
	Region    string
	PathStyle bool
}

// Client wraps the built, bucket-agnostic S3 client. The concrete *s3.Client is
// exposed (S3) because consumers layer their own bucket/object semantics on it.
type Client struct {
	S3 *s3.Client
}

// New builds a Client from the standard config chain. It does NOT touch the network.
func New(ctx context.Context, cfg Config) (*Client, error) {
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("objectstore: load config: %w", err)
	}
	cl := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.PathStyle
	})
	return &Client{S3: cl}, nil
}

// ConfigFromEnv resolves the object-store connection from the environment, in ONE
// place. STRATT_OBJECTSTORE_* is canonical; STRATT_EVIDENCE_* and STRATT_AWS_* are
// honored as backward-compatible fallbacks (the object store historically shared the
// EC2 mock's endpoint — that fallback is preserved so no deployment breaks, but a
// dedicated STRATT_OBJECTSTORE_ENDPOINT now severs the coupling). PathStyle defaults
// on (S3-compatible dev backends need it); set STRATT_OBJECTSTORE_PATH_STYLE=false for
// virtual-host-style AWS.
func ConfigFromEnv() Config {
	return Config{
		Endpoint:  firstNonEmpty(os.Getenv("STRATT_OBJECTSTORE_ENDPOINT"), os.Getenv("STRATT_EVIDENCE_ENDPOINT"), os.Getenv("STRATT_AWS_ENDPOINT")),
		Region:    firstNonEmpty(os.Getenv("STRATT_OBJECTSTORE_REGION"), os.Getenv("STRATT_EVIDENCE_REGION"), os.Getenv("STRATT_AWS_REGION"), "us-east-1"),
		PathStyle: os.Getenv("STRATT_OBJECTSTORE_PATH_STYLE") != "false",
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
