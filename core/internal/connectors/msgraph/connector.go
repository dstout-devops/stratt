// Package msgraph is the Microsoft Graph Connector (charter §2.2, §8 Phase 1
// breadth): a core-tier, in-tree Connector shipping one capability, a Syncer
// over Entra directory devices.
//
// Transport: the native Graph REST API with delta queries — the vendor's
// change-feed transport, satisfying §2.2's full-fidelity requirement for
// Syncers. Auth is OAuth2 client credentials.
//
// Projection path (§1.2): observations flow through this package's
// Normalizer into the graph.Projector's normalizer write path — nothing here
// writes the graph any other way.
package msgraph

import (
	"context"
	"net/http"
	"strings"

	"golang.org/x/oauth2/clientcredentials"

	"github.com/dstout-devops/stratt/types"
)

// Config locates the Graph Source. Credentials arrive resolved from process
// env at startup (the vCenter posture; CredentialRef brokering for Syncers
// is the recorded ADR-0009 follow-up) — material is never persisted (§2.5).
type Config struct {
	// Endpoint is the Graph base URL including version, default
	// https://graph.microsoft.com/v1.0 (overridable for the dev sim).
	Endpoint string
	TenantID string
	ClientID string
	// ClientSecret is used only to mint tokens; never stored.
	ClientSecret string
	// TokenURL overrides the tenant-derived token endpoint (dev sim).
	TokenURL string
	// SourceName names the registered Source and scopes writer identity.
	SourceName string
}

// SyncerRef is the Syncer's writer identity for Provenance and the
// facet-ownership registry.
func (c Config) SyncerRef() string {
	return "connector/msgraph/" + c.SourceName + "/syncer"
}

// FacetNamespaces are the Facet namespaces this Syncer owns (§2.1 — one
// declared writer, registered before the first projection).
func (c Config) FacetNamespaces() []types.FacetOwner {
	owner := func(ns string) types.FacetOwner {
		return types.FacetOwner{Namespace: ns, OwnerKind: "syncer", OwnerRef: c.SyncerRef()}
	}
	return []types.FacetOwner{
		owner("device.identity"), // azureAdDeviceId, trustType, profileType
		owner("device.os"),       // operatingSystem, version
		owner("device.state"),    // accountEnabled, approximate last sign-in
	}
}

func (c Config) tokenURL() string {
	if c.TokenURL != "" {
		return c.TokenURL
	}
	return "https://login.microsoftonline.com/" + c.TenantID + "/oauth2/v2.0/token"
}

// scope derives the .default scope from the endpoint origin, so the sim and
// the real service both get a coherent value.
func (c Config) scope() string {
	origin := c.Endpoint
	if i := strings.Index(origin, "/v1.0"); i > 0 {
		origin = origin[:i]
	}
	return origin + "/.default"
}

// httpClient returns an OAuth2 client-credentials transport that mints and
// refreshes tokens as needed.
func (c Config) httpClient(ctx context.Context) *http.Client {
	cc := clientcredentials.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		TokenURL:     c.tokenURL(),
		Scopes:       []string{c.scope()},
	}
	return cc.Client(ctx)
}
