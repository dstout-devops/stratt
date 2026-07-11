// Package vcenter is the Phase-0 vCenter-class Connector (charter §2.2, §8):
// a core-tier, in-tree Connector shipping one capability, a Syncer.
//
// Transport: the SOAP vim25 API via govmomi (ADR-0007). vSphere's
// PropertyCollector is the vendor's only native transport with change-feed
// semantics, satisfying §2.2's full-fidelity requirement for Syncers.
//
// Projection path (§1.2): Syncer observations flow through the Normalizer in
// this package into the graph.Projector's normalizer write path — nothing
// here writes the graph any other way.
package vcenter

import (
	"context"
	"fmt"
	"net/url"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"

	"github.com/dstout-devops/stratt/types"
)

// Config locates the vCenter Source. Credentials arrive resolved (user/pass)
// from the CredentialRef injection path — for Phase 0 that is process env at
// startup; material is never persisted (§2.5).
type Config struct {
	// Endpoint is the SDK URL, e.g. https://vcenter.example/sdk (the /sdk
	// path is appended when absent).
	Endpoint string
	Username string
	Password string
	// Insecure skips TLS verification — dev/vcsim only.
	Insecure bool
	// SourceName names the registered Source; also scopes the Syncer's
	// writer identity.
	SourceName string
}

// SyncerRef is the Syncer's writer identity for Provenance and the
// facet-ownership registry.
func (c Config) SyncerRef() string {
	return "connector/vcenter/" + c.SourceName + "/syncer"
}

// FacetNamespaces are the Facet namespaces this Syncer owns and writes.
// Registered (single-owner, §2.1) before the first projection.
func (c Config) FacetNamespaces() []types.FacetOwner {
	owner := func(ns string) types.FacetOwner {
		return types.FacetOwner{Namespace: ns, OwnerKind: "syncer", OwnerRef: c.SyncerRef()}
	}
	return []types.FacetOwner{
		owner("vm.config"),  // cpus, memoryMB, guest id, tools status
		owner("vm.runtime"), // power state, connection state
		owner("net.guest"),  // guest-reported hostname / IP addresses
	}
}

// connect dials the vim25 endpoint and authenticates.
func connect(ctx context.Context, cfg Config) (*govmomi.Client, error) {
	u, err := soap.ParseURL(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("vcenter: parse endpoint: %w", err)
	}
	u.User = url.UserPassword(cfg.Username, cfg.Password)
	client, err := govmomi.NewClient(ctx, u, cfg.Insecure)
	if err != nil {
		return nil, fmt.Errorf("vcenter: connect %s: %w", u.Host, err)
	}
	return client, nil
}

// about returns the endpoint's identity line (used for Source registration
// sanity and logs).
func about(v *vim25.Client) string {
	a := v.ServiceContent.About
	return fmt.Sprintf("%s %s (%s)", a.FullName, a.Version, a.InstanceUuid)
}
