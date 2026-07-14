package chef

import (
	"encoding/json"
	"fmt"
	"strings"

	chefapi "github.com/go-chef/chef"

	"github.com/dstout-devops/stratt/core/internal/graph"
)

// normalizeNode maps one Chef node (ohai automatic attributes) onto the graph
// shape. Pure function — all writes go through the Projector in the Syncer.
//
// Identity: chef.node.name is always present; dns.fqdn (from ohai) is added
// when known so a Chef-sourced host correlates with the same host observed by
// vCenter/msgraph (correlation is by identity-key overlap in the Projector).
//
// Facets are curated charter-down from ohai (never a dump) onto connector-
// namespaced observed facets (the msgraph device.* precedent); empty facets are
// omitted. Labels power View selection (the AAP-inventory → Stratt-View story).
func normalizeNode(n chefapi.Node) (graph.EntityUpsert, error) {
	if n.Name == "" {
		return graph.EntityUpsert{}, fmt.Errorf("chef: node has no name; cannot project without identity")
	}
	auto := n.AutomaticAttributes // may be nil on a never-converged node

	identity := map[string]string{"chef.node.name": n.Name}
	if fqdn := str(auto, "fqdn"); fqdn != "" {
		identity["dns.fqdn"] = fqdn
	}

	labels := map[string]string{"chef.node.name": n.Name}
	if n.Environment != "" {
		labels["chef.environment"] = n.Environment
	}
	// One boolean label per assigned role, so a View can select "hosts running
	// role X". Expanded automatic.roles is a documented enrichment (v1 reads the
	// node's own run_list role[...] entries).
	for _, entry := range n.RunList {
		if role, ok := roleName(entry); ok {
			labels["chef.role."+role] = "true"
		}
	}

	nodeIdentity := prune(map[string]any{
		"platform":         str(auto, "platform"),
		"platform_family":  str(auto, "platform_family"),
		"platform_version": str(auto, "platform_version"),
		"chef_client":      chefClientVersion(auto),
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

	facets := map[string]json.RawMessage{}
	for ns, doc := range map[string]map[string]any{
		"node.identity": nodeIdentity,
		"node.os":       nodeOS,
		"node.network":  nodeNetwork,
	} {
		if len(doc) == 0 {
			continue
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return graph.EntityUpsert{}, fmt.Errorf("chef: marshal facet %s: %w", ns, err)
		}
		facets[ns] = raw
	}

	return graph.EntityUpsert{
		Kind:         "host",
		IdentityKeys: identity,
		Labels:       labels,
		Facets:       facets,
	}, nil
}

// roleName extracts X from a "role[X]" run_list entry.
func roleName(entry string) (string, bool) {
	if strings.HasPrefix(entry, "role[") && strings.HasSuffix(entry, "]") {
		return entry[len("role[") : len(entry)-1], true
	}
	return "", false
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
