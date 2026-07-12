// Package awsec2 is the EC2 cloud-instance Connector (charter §2.2, §8
// Phase-1 breadth): a core-tier, in-tree Connector shipping one capability,
// a Syncer over EC2 instances.
//
// Transport: the vendor's native API via the official aws-sdk-go-v2
// (dependency-scout RECOMMEND, ADR-0014). EC2 exposes no change feed, so
// each cycle is an honest paginated full enumeration + tombstone-absent —
// recorded, not hidden.
//
// Projection path (§1.2): observations flow through this package's
// Normalizer into the graph.Projector's normalizer write path only.
package awsec2

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/dstout-devops/stratt/types"
)

// Config locates the EC2 Source. Credentials arrive via the SDK's standard
// environment chain (AWS_ACCESS_KEY_ID etc.) — the env-stub posture shared
// with the other Syncers; material is never persisted (§2.5).
type Config struct {
	// Endpoint overrides the API endpoint (dev: the moto stand-in).
	Endpoint string
	Region   string
	// SourceName names the registered Source and scopes writer identity.
	SourceName string
}

// SyncerRef is the Syncer's writer identity for Provenance and the
// facet-ownership registry.
func (c Config) SyncerRef() string {
	return "connector/awsec2/" + c.SourceName + "/syncer"
}

// FacetNamespaces are the Facet namespaces this Syncer owns (§2.1).
func (c Config) FacetNamespaces() []types.FacetOwner {
	owner := func(ns string) types.FacetOwner {
		return types.FacetOwner{Namespace: ns, OwnerKind: "syncer", OwnerRef: c.SyncerRef()}
	}
	return []types.FacetOwner{
		owner("instance.compute"), // instanceType, architecture, imageId
		owner("instance.network"), // ips, vpc, subnet, az
		owner("instance.state"),   // lifecycle state, launch time
	}
}

// client builds the EC2 API client from the standard config chain.
func (c Config) client(ctx context.Context) (*ec2.Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(c.Region))
	if err != nil {
		return nil, fmt.Errorf("awsec2: load config: %w", err)
	}
	return ec2.NewFromConfig(cfg, func(o *ec2.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
		}
	}), nil
}
