// Package puppet is the OpenVox/PuppetDB Syncer plugin: the PuppetDB content-
// expertise that used to live in core/internal/connectors/puppet, now behind the
// sovereign plugin port (ADR-0046/0047). It maps PuppetDB /inventory rows to
// core-legible ObservedEntity wire values; the core-side host governs what it may
// write (ownership, identity gating, provenance). The plugin holds no graph write
// path.
package puppet

import (
	"encoding/json"
	"fmt"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// inventoryEntry is one /pdb/query/v4/inventory row — the subset the plugin
// consumes. Facts holds Facter structured facts (nested os/networking hashes).
type inventoryEntry struct {
	Certname    string         `json:"certname"`
	Environment string         `json:"environment"`
	Facts       map[string]any `json:"facts"`
	Trusted     map[string]any `json:"trusted"`
}

// normalizeNode maps one PuppetDB inventory entry onto an ObservedEntity. Pure
// content-expertise — the plugin proposes; the core-side host decides what to
// write.
//
// Identity: puppet.certname is always present; dns.fqdn (from networking.fqdn)
// is added when known so a Puppet-sourced host correlates with the same host
// observed by Chef/vCenter/msgraph. dns.fqdn is a SHARED cross-source identity
// scheme — the plugin only proposes it; the core-side host gates whether this
// plugin (by tier + grant) may WRITE that cross-source scheme (ADR-0046 finding
// #4). Correlation is by identity-key overlap in the host — NOT shared facet
// namespaces, which stay source-scoped.
//
// Facets are curated charter-down from Facter (never a dump) onto SOURCE-scoped
// observed facets (puppet.node.*, mirroring chef.node.*); empty facets omitted.
func normalizeNode(e inventoryEntry) (*pluginv1.ObservedEntity, error) {
	if e.Certname == "" {
		return nil, fmt.Errorf("puppet: inventory entry has no certname; cannot project without identity")
	}
	facts := e.Facts
	os := submap(facts, "os")
	networking := submap(facts, "networking")
	fqdn := str(networking, "fqdn")

	identity := map[string]string{"puppet.certname": e.Certname}
	if fqdn != "" {
		identity["dns.fqdn"] = fqdn
	}

	// No Entity labels: the Entity `labels` bag is a whole-set last-writer
	// projection, so two Sources correlating onto one host (Chef + Puppet on a
	// shared dns.fqdn) would clobber each other's labels — implicit precedence
	// across Sources (§2.4). Source-attributable, selectable data lives in the
	// SOURCE-scoped facets instead (which have per-namespace ownership); Views
	// select on those facets (ADR-0038). `environment` rides the identity facet.
	nodeIdentity := prune(map[string]any{
		"os_name":         str(os, "name"),
		"os_family":       str(os, "family"),
		"os_version":      str(submap(os, "release"), "full"),
		"os_architecture": str(os, "architecture"),
		"environment":     e.Environment,
	})
	nodeOS := prune(map[string]any{
		"kernel":        str(facts, "kernel"),
		"kernelrelease": str(facts, "kernelrelease"),
		"kernelversion": str(facts, "kernelversion"),
	})
	nodeNetwork := prune(map[string]any{
		"fqdn":       fqdn,
		"ipv4":       str(networking, "ip"),
		"ipv6":       str(networking, "ip6"),
		"macaddress": str(networking, "mac"),
	})

	facets := map[string][]byte{}
	for ns, doc := range map[string]map[string]any{
		"puppet.node.identity": nodeIdentity,
		"puppet.node.os":       nodeOS,
		"puppet.node.network":  nodeNetwork,
	} {
		if len(doc) == 0 {
			continue
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("puppet: marshal facet %s: %w", ns, err)
		}
		facets[ns] = raw
	}

	return &pluginv1.ObservedEntity{Kind: "host", IdentityKeys: identity, Facets: facets}, nil
}

// str returns m[key] as a string, or "" if absent/other-typed/nil-map.
func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// submap returns m[key] as a nested map, or nil.
func submap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

// prune drops empty-string values so absent facts don't project as "".
func prune(m map[string]any) map[string]any {
	for k, v := range m {
		if s, ok := v.(string); ok && s == "" {
			delete(m, k)
		}
	}
	return m
}
