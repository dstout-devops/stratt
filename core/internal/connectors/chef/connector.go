// Package chef is the Chef Infra Server Connector (charter §2.2): a core-tier,
// in-tree Connector shipping one capability — a Syncer over Chef node objects
// (ohai automatic attributes) projected into the estate graph.
//
// Transport: the native Chef Infra Server REST API. Chef has no change feed,
// so the Syncer enumerates all nodes on an interval and tombstones what a
// Source no longer reports (the full-enumeration path, not a delta cursor).
// Auth is Chef's Mixlib signed-header scheme (RSA-signed X-Ops-* headers);
// that hazardous crypto surface is delegated to github.com/go-chef/chef rather
// than reimplemented (ADR-0037, dependency-scout RECOMMEND) — the vendor-native
// posture already taken for aws-sdk-go-v2 (EC2) and govmomi (vCenter).
//
// Projection path (§1.2): observations flow through this package's Normalizer
// into the graph.Projector's normalizer write path — nothing here writes the
// graph any other way. Chef stays the authoritative system of record; the
// graph is a rebuildable read-model. Not a writable CMDB.
//
// Framing (ADR-0037): Chef Infra Server (OSS) is EOL Nov 2026, consolidating
// into Chef 360; the node-API surface read here is stable across legacy Infra
// Server, Chef 360 Self-Managed, and the CINC OSS rebuild. This Syncer is the
// strangler-fig capture wedge (§7.6) — lift the estate into the graph now.
package chef

import (
	"fmt"

	chefapi "github.com/go-chef/chef"

	"github.com/dstout-devops/stratt/types"
)

// Config locates a Chef Infra Server Source. Credentials arrive resolved from
// process env at startup (the vCenter/msgraph posture; CredentialRef brokering
// for Syncers is the recorded ADR-0009 follow-up) — the PEM key is used only to
// sign requests and is never persisted (§2.5).
type Config struct {
	// ServerURL is the org-scoped Chef Infra Server base URL, terminated with a
	// slash per Chef convention, e.g. https://chef.example.com/organizations/acme/
	ServerURL string
	// ClientName is the Chef API client (user) id signing requests.
	ClientName string
	// KeyPEM is the plaintext RSA private key (PEM) for that client; sign-only,
	// never stored.
	KeyPEM string
	// AuthVersion selects the Mixlib sign protocol ("1.0" default, "1.3"
	// available). Legacy Chef servers (e.g. Chef 15) negotiate 1.0.
	AuthVersion string
	// SkipSSL disables TLS verification, for self-signed legacy Chef servers.
	SkipSSL bool
	// SourceName names the registered Source and scopes writer identity.
	SourceName string
}

// SyncerRef is the Syncer's writer identity for Provenance and the
// facet-ownership registry.
func (c Config) SyncerRef() string {
	return "connector/chef/" + c.SourceName + "/syncer"
}

// FacetNamespaces are the Facet namespaces this Syncer owns (§2.1 — one
// declared writer, registered before the first projection). Curated
// charter-down from ohai (never a dump); left uncovered by a pinned schema
// until a shipping Contract demands one (§1.1), exactly as msgraph's device.*.
//
// Namespaces are SOURCE-scoped (chef.node.*, not a shared node.*): §2.1's
// one-owner-per-namespace registry forbids a second config-mgmt Syncer
// (e.g. puppet) co-owning them, and a shared namespace would be last-writer-
// wins across Sources — the implicit precedence §2.4 bans. Cross-source hosts
// still unify via the dns.fqdn identity key; unified fact queries are a future
// normalization layer, not two Syncers fighting one namespace (ADR-0038).
func (c Config) FacetNamespaces() []types.FacetOwner {
	owner := func(ns string) types.FacetOwner {
		return types.FacetOwner{Namespace: ns, OwnerKind: "syncer", OwnerRef: c.SyncerRef()}
	}
	return []types.FacetOwner{
		owner("chef.node.identity"), // platform, platform_family, platform_version, chef_client
		owner("chef.node.os"),       // kernel name/release/machine, uptime
		owner("chef.node.network"),  // fqdn, primary ipv4/ipv6, default gateway
	}
}

func (c Config) authVersion() chefapi.AuthVersion {
	if c.AuthVersion == "1.3" {
		return chefapi.AuthVersion13
	}
	return chefapi.AuthVersion10
}

// chefClient builds the signing Chef API client. The library installs an
// RSA-signing round-tripper (the Chef analog of msgraph wrapping an oauth2
// transport); we do not hold or reimplement the Mixlib crypto.
func (c Config) chefClient() (*chefapi.Client, error) {
	client, err := chefapi.NewClient(&chefapi.Config{
		Name:                  c.ClientName,
		Key:                   c.KeyPEM,
		BaseURL:               c.ServerURL,
		SkipSSL:               c.SkipSSL,
		AuthenticationVersion: c.authVersion(),
	})
	if err != nil {
		return nil, fmt.Errorf("chef: build client for %q: %w", c.SourceName, err)
	}
	return client, nil
}
