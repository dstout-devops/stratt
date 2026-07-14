// Package puppet is the OpenVox/PuppetDB Connector (charter §2.2): a core-tier,
// in-tree Connector shipping one capability — a Syncer over PuppetDB node facts
// (Facter output) projected into the estate graph.
//
// Transport: the PuppetDB-compatible v4 query API (/pdb/query/v4). OpenVoxDB is
// an API-compatible Apache-2.0 fork of PuppetDB (charter §0: Puppet forked to
// OpenVox after Perforce restricted binaries); this Syncer treats them as one
// Source contract with a configurable base URL — never a hardcoded vendor.
// Facts have no change feed, so the Syncer enumerates the /inventory endpoint
// on an interval and tombstones what a Source no longer reports.
//
// Auth is stdlib mTLS: PuppetDB validates a client certificate against the
// Puppet CA + a certificate-allowlist. No third-party library is needed
// (contrast: Chef's Mixlib RSA signing forced go-chef) — crypto/tls handles it
// natively. This is itself the point of the config-mgmt generality test: the
// abstraction generalizes, not a vendor lib.
//
// Projection path (§1.2): observations flow through this package's Normalizer
// into the graph.Projector's normalizer write path — nothing here writes the
// graph any other way. PuppetDB/OpenVox stays the authoritative SoR; the graph
// is a rebuildable read-model. Not a writable CMDB.
package puppet

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// Config locates a PuppetDB-compatible Source. Client-certificate material
// arrives from mounted files at startup (the vCenter/msgraph/chef posture;
// CredentialRef brokering for Syncers is the recorded ADR-0009 follow-up) — it
// is used only to establish mTLS and is never persisted (§2.5).
type Config struct {
	// BaseURL is the PuppetDB/OpenVoxDB base, e.g. https://puppetdb:8081 (mTLS)
	// or http://localhost:8080 (dev, no cert).
	BaseURL string
	// CertFile / KeyFile / CAFile are the mTLS client cert, key, and the Puppet
	// CA to trust. Required for an https:// BaseURL; ignored for http://.
	CertFile string
	KeyFile  string
	CAFile   string
	// SourceName names the registered Source and scopes writer identity.
	SourceName string
}

// SyncerRef is the Syncer's writer identity for Provenance and the
// facet-ownership registry.
func (c Config) SyncerRef() string {
	return "connector/puppet/" + c.SourceName + "/syncer"
}

// FacetNamespaces are the Facet namespaces this Syncer owns (§2.1 — one
// declared writer). SOURCE-scoped (puppet.node.*, mirroring chef.node.*):
// §2.1's one-owner registry forbids two config-mgmt Syncers sharing a namespace,
// and a shared namespace would be last-writer-wins across Sources (the §2.4
// implicit-precedence ban). Cross-source hosts unify via dns.fqdn instead
// (ADR-0038). Curated charter-down from Facter; uncovered by a pinned schema
// until a shipping Contract demands one (§1.1).
func (c Config) FacetNamespaces() []types.FacetOwner {
	owner := func(ns string) types.FacetOwner {
		return types.FacetOwner{Namespace: ns, OwnerKind: "syncer", OwnerRef: c.SyncerRef()}
	}
	return []types.FacetOwner{
		owner("puppet.node.identity"), // os name/family/release/architecture
		owner("puppet.node.os"),       // kernel, kernelrelease, kernelversion
		owner("puppet.node.network"),  // fqdn, primary ipv4/ipv6, mac
	}
}

// httpClient builds a stdlib client. For an https:// BaseURL it configures mTLS
// (client cert + the Puppet CA trust pool); for http:// (localhost dev) it
// returns a plain client. No third-party dependency (§1.4).
func (c Config) httpClient() (*http.Client, error) {
	if strings.HasPrefix(c.BaseURL, "http://") {
		return &http.Client{Timeout: 30 * time.Second}, nil
	}
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("puppet: load client cert for %q: %w", c.SourceName, err)
	}
	pool := x509.NewCertPool()
	caPEM, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, fmt.Errorf("puppet: read CA %q: %w", c.CAFile, err)
	}
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("puppet: CA file %q held no valid certificates", c.CAFile)
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{cert},
				RootCAs:      pool,
				MinVersion:   tls.VersionTLS12,
			},
		},
	}, nil
}
