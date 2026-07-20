// Package salt is the Salt Syncer plugin: the salt-api grains content-expertise
// that used to live in core/internal/connectors/salt, now behind the sovereign
// plugin port (ADR-0046/0047). It maps minion grains to core-legible
// ObservedEntity wire values; the core-side host governs what it may write
// (ownership, identity gating, provenance). The plugin holds no graph write path.
//
// Scope: the Syncer only (the runner cache.grains enumeration → host entities).
// The salt Emitter (event-bus/Subscribe) is deliberately NOT extracted — it
// stays in-tree until the host handles the EmittedEvent path (a later slice).
package salt

import (
	"encoding/json"
	"fmt"
	"sort"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// attachPackages sets a software.package Facet (ADR-0080 slice 2b) on each observed
// entity whose minion reported an installed-package list. Correlated by the salt
// minion id the Syncer already keys on. A minion with no packages is left untouched.
func attachPackages(entities []*pluginv1.ObservedEntity, pkgsByMinion map[string]map[string]string) {
	for _, e := range entities {
		pkgs, ok := pkgsByMinion[e.GetIdentityKeys()["salt.minion_id"]]
		if !ok || len(pkgs) == 0 {
			continue
		}
		if e.Facets == nil {
			e.Facets = map[string][]byte{}
		}
		e.Facets["software.package"] = softwarePackageFacet(pkgs)
	}
}

// softwarePackageFacet renders a minion's {package: version} map into the
// software.package Facet — the package form of the deliverable-software dimension
// (ADR-0080): origin "distro", deliveryForm "package". Deterministic order (sorted
// by name) so the projection is stable across cycles and tests.
func softwarePackageFacet(pkgs map[string]string) []byte {
	names := make([]string, 0, len(pkgs))
	for name := range pkgs {
		names = append(names, name)
	}
	sort.Strings(names)
	list := make([]map[string]any, 0, len(names))
	for _, name := range names {
		list = append(list, map[string]any{
			"name":         name,
			"version":      pkgs[name],
			"origin":       "distro",
			"deliveryForm": "package",
		})
	}
	raw, _ := json.Marshal(map[string]any{"packages": list})
	return raw
}

// normalizeMinion maps one minion's grains onto an ObservedEntity. Pure
// content-expertise — no graph writes (the plugin holds no DB path).
//
// Identity: salt.minion_id (the enumeration map key) is always present; dns.fqdn
// (from the fqdn grain) is emitted when known so a Salt-sourced host correlates
// with the same host observed by Chef/Puppet/vCenter. dns.fqdn is a SHARED,
// cross-source identity scheme — the plugin only PROPOSES it; the core-side host
// decides whether this plugin (by tier + grant) may WRITE that scheme (ADR-0046
// finding #4, ADR-0047 §1). The source-scoped salt.node.* facets stay this
// plugin's own.
//
// NO Entity labels: the label bag is a whole-set last-writer projection that
// clobbers across Sources correlating onto one host (§2.4, ADR-0038). Selectable
// data lives in the source-scoped facets; Views select on those facets.
func normalizeMinion(minionID string, grains map[string]any) (*pluginv1.ObservedEntity, error) {
	if minionID == "" {
		return nil, fmt.Errorf("salt: minion has no id; cannot project without identity")
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

	facets := map[string][]byte{}
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
			return nil, fmt.Errorf("salt: marshal facet %s: %w", ns, err)
		}
		facets[ns] = raw
	}

	return &pluginv1.ObservedEntity{
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
