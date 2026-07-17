// Package chef is the Chef Syncer plugin: the go-chef content-expertise that
// used to live in core/internal/connectors/chef, now behind the sovereign plugin
// port (ADR-0046/0047). It maps Chef node objects (ohai automatic attributes) to
// core-legible ObservedEntity wire values; the core-side host governs what it may
// write (ownership, identity gating, provenance). The plugin holds no graph write
// path.
package chef

import (
	"encoding/json"
	"fmt"

	chefapi "github.com/go-chef/chef"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// normalizeNode maps one Chef node (ohai automatic attributes) to an
// ObservedEntity. Pure content-expertise; no graph writes (the plugin holds no
// DB path).
//
// Identity: chef.node.name is always present; the plugin also emits dns.fqdn
// (from ohai) when known — it is the content-expert. The core-side host decides
// whether this plugin (by tier + grant) may WRITE that cross-source identity
// scheme (ADR-0046 finding #4); the plugin only proposes. A Chef-sourced host
// then correlates with the same host observed by vCenter/msgraph.
//
// Facets are curated charter-down from ohai (never a dump) onto connector-
// namespaced observed facets; empty facets are omitted. No Entity labels: the
// label bag is a whole-set last-writer projection that would clobber across
// Sources correlating onto one host (§2.4), so source-attributable data lives in
// the source-scoped facets instead.
func normalizeNode(n chefapi.Node) (*pluginv1.ObservedEntity, error) {
	if n.Name == "" {
		return nil, fmt.Errorf("chef: node has no name; cannot project without identity")
	}
	auto := n.AutomaticAttributes // may be nil on a never-converged node

	identity := map[string]string{"chef.node.name": n.Name}
	if fqdn := str(auto, "fqdn"); fqdn != "" {
		identity["dns.fqdn"] = fqdn
	}

	nodeIdentity := prune(map[string]any{
		"platform":         str(auto, "platform"),
		"platform_family":  str(auto, "platform_family"),
		"platform_version": str(auto, "platform_version"),
		"chef_client":      chefClientVersion(auto),
		"environment":      n.Environment,
	})
	kernel := submap(auto, "kernel")
	nodeOS := prune(map[string]any{
		"os":             str(auto, "os"),
		"kernel_name":    str(kernel, "name"),
		"kernel_release": str(kernel, "release"),
		"kernel_machine": str(kernel, "machine"),
		"uptime":         str(auto, "uptime"),
	})
	nodeNetwork := prune(map[string]any{
		"fqdn":       str(auto, "fqdn"),
		"ipv4":       str(auto, "ipaddress"),
		"ipv6":       str(auto, "ip6address"),
		"macaddress": str(auto, "macaddress"),
	})

	facets := map[string][]byte{}
	for ns, doc := range map[string]map[string]any{
		"chef.node.identity": nodeIdentity,
		"chef.node.os":       nodeOS,
		"chef.node.network":  nodeNetwork,
	} {
		if len(doc) == 0 {
			continue
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("chef: marshal facet %s: %w", ns, err)
		}
		facets[ns] = raw
	}

	return &pluginv1.ObservedEntity{
		Kind:         "host",
		IdentityKeys: identity,
		Facets:       facets,
	}, nil
}

// chefClientVersion reads ohai chef_packages.chef.version when present.
func chefClientVersion(auto map[string]any) string {
	return str(submap(submap(auto, "chef_packages"), "chef"), "version")
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

// prune drops empty-string values so absent ohai facts don't project as "".
func prune(m map[string]any) map[string]any {
	for k, v := range m {
		if s, ok := v.(string); ok && s == "" {
			delete(m, k)
		}
	}
	return m
}
