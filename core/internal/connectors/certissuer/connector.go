// Package certissuer is the cert-issuer Connector (charter §2.2, §2.4
// Intent/Certificate GA, ADR-0030): a core-tier, in-tree Connector over a
// Vault-compatible PKI CLM. The dev transport is OpenBao (MPL-2.0, the
// rug-pull-proof Vault fork); the Connector contract is issuer-agnostic (§1.5),
// so step-ca / cert-manager / Vault satisfy it later — OpenBao is never
// load-bearing ("any PKI-compatible CLM, never OpenBao-by-name").
//
// This package ships the Syncer capability (projection §1.2): enumerate issued
// certs → parse X.509 → project `cert` Entities with cert.identity/cert.expiry
// Facets and provenance. The write-side issue/renew/revoke operation is the
// separate cert-issuer Actuator, so its credential is injected into an
// execution pod at spawn (§2.5), never the control plane.
package certissuer

import "github.com/dstout-devops/stratt/types"

// Config locates the CLM Source. The token is the read-side projection
// credential — resolved from the environment chain like the other Syncers
// (STRATT_CLM_TOKEN); material is never persisted (§2.5).
type Config struct {
	// Addr is the CLM base URL (dev: OpenBao on :8200).
	Addr string
	// Token authenticates enumeration/read (X-Vault-Token).
	Token string
	// Mount is the PKI secrets-engine mount (default "pki").
	Mount string
	// SourceName names the registered Source and scopes writer identity.
	SourceName string
}

// SyncerRef is the Syncer's writer identity for Provenance and the
// facet-ownership registry.
func (c Config) SyncerRef() string {
	return "connector/certissuer/" + c.SourceName + "/syncer"
}

// FacetNamespaces are the Facet namespaces this Syncer owns (§2.1).
func (c Config) FacetNamespaces() []types.FacetOwner {
	owner := func(ns string) types.FacetOwner {
		return types.FacetOwner{Namespace: ns, OwnerKind: "syncer", OwnerRef: c.SyncerRef()}
	}
	return []types.FacetOwner{
		owner("cert.identity"), // commonName, serialNumber, issuer, dnsNames
		owner("cert.expiry"),   // notBefore, notAfter
	}
}

// LabelOwners are the Entity-label keys this Syncer owns (§2.1, ADR-0038).
func (c Config) LabelOwners() []types.LabelOwner {
	return []types.LabelOwner{
		{Key: "cert.commonName", OwnerKind: "syncer", OwnerRef: c.SyncerRef()},
	}
}
