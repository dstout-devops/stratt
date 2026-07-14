package salt

import (
	"encoding/json"
	"fmt"

	"github.com/dstout-devops/stratt/core/internal/graph"
)

// normalizeMinion maps one minion's grains onto the graph shape. Pure function
// — all writes go through the Projector in the Syncer.
//
// Identity: salt.minion_id (the enumeration map key) is always present; dns.fqdn
// (from the fqdn grain) is added when known so a Salt-sourced host correlates
// with the same host observed by Chef/Puppet/vCenter (identity-key overlap in
// the Projector — NOT shared facet namespaces, which stay source-scoped).
//
// NO Entity labels: the label bag is a whole-set last-writer projection that
// clobbers across Sources correlating onto one host (§2.4, ADR-0038). Selectable
// data lives in the source-scoped facets; Views select on those facets.
func normalizeMinion(minionID string, grains map[string]any) (graph.EntityUpsert, error) {
	if minionID == "" {
		return graph.EntityUpsert{}, fmt.Errorf("salt: minion has no id; cannot project without identity")
	}
	fqdn := str(grains, "fqdn")

	identity := map[string]string{"salt.minion_id": minionID}
	if fqdn != "" {
		identity["dns.fqdn"] = fqdn
	}

	nodeIdentity := prune(map[string]any{
		"os":          str(grains, "os"),
		"os_family":   str(grains, "os_family"),
		"osfinger":    str(grains, "osfinger"),
		"osrelease":   str(grains, "osrelease"),
		"machine_id":  str(grains, "machine_id"),
		"saltversion": str(grains, "saltversion"),
	})
	nodeOS := prune(map[string]any{
		"kernel":        str(grains, "kernel"),
		"kernelrelease": str(grains, "kernelrelease"),
		"kernelversion": str(grains, "kernelversion"),
		"cpuarch":       str(grains, "cpuarch"),
	})
	nodeNetwork := prune(map[string]any{
		"ipv4":     firstStr(grains, "ipv4"),
		"ipv6":     firstStr(grains, "ipv6"),
		"fqdn_ip4": firstStr(grains, "fqdn_ip4"),
	})

	facets := map[string]json.RawMessage{}
	for ns, doc := range map[string]map[string]any{
		"salt.node.identity": nodeIdentity,
		"salt.node.os":       nodeOS,
		"salt.node.network":  nodeNetwork,
	} {
		if len(doc) == 0 {
			continue
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return graph.EntityUpsert{}, fmt.Errorf("salt: marshal facet %s: %w", ns, err)
		}
		facets[ns] = raw
	}

	return graph.EntityUpsert{
		Kind:         "host",
		IdentityKeys: identity,
		Facets:       facets,
	}, nil
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

// firstStr returns the first string element of a list-valued grain (ipv4/ipv6/
// fqdn_ip4 are lists), or "" — handling both []any (JSON-decoded) and []string.
func firstStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				return s
			}
		}
	case []string:
		for _, s := range v {
			if s != "" {
				return s
			}
		}
	case string:
		return v
	}
	return ""
}

// prune drops empty-string values so absent grains don't project as "".
func prune(m map[string]any) map[string]any {
	for k, v := range m {
		if s, ok := v.(string); ok && s == "" {
			delete(m, k)
		}
	}
	return m
}
