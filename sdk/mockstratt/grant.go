package mockstratt

// The operator grant — the authority half of the port, mirrored from the core so a
// plugin can develop against a REAL ceiling rather than an imagined one.
//
// The grant is the single source of truth for what a plugin may own and emit
// (ADR-0046 findings #1/#4/#6). The Manifest is a REQUEST that must match it; a
// plugin never self-grants, which is what stops connection order from deciding who
// owns a namespace (§2.4, no implicit precedence). A plugin author's most common
// surprise — "my facet write-back vanished" — is almost always this, and the whole
// point of surfacing it here is that the surprise happens on their laptop.

// Tier is a plugin's trust tier, established at registration from its signature.
// It gates the single most dangerous capability — cross-source identity emission.
type Tier string

const (
	TierCommunity Tier = "community"
	TierTrusted   Tier = "trusted"
)

// SharedIdentitySchemes are cross-source correlation schemes: a value under one of
// these can merge Entities observed by DIFFERENT Sources (a vCenter VM and a Chef
// node sharing a DNS name). Emitting one is estate-wide power, so a COMMUNITY-tier
// plugin may never emit them even when the operator grant lists the scheme — tier
// AND grant, defence in depth (ADR-0046 finding #4). Vendor-namespaced schemes
// (vcenter.uuid, aws.instanceId) are source-local and safe.
var SharedIdentitySchemes = map[string]bool{
	"dns.fqdn": true,
	"mac":      true,
}

// Grant is the operator-declared authority for one plugin, keyed on its
// authenticated channel identity.
//
// This is a deliberate near-copy of core's pluginhost.Grant with the graph-facing
// fields dropped: a plugin can observe the CEILING (what is refused) but never the
// registration (Source rows, ownership registry, home Cell), because none of that
// crosses the port. Copying rather than importing is the dependency direction the
// whole ADR is about — a plugin that imported core to test itself would prove the
// opposite of what this package exists to prove.
type Grant struct {
	// PluginIdentity is the authenticated channel identity. A Manifest whose
	// plugin_id differs fails registration — the anti-spoof binding (ADR-0049 F1).
	PluginIdentity string
	Tier           Tier
	// SourceName is the operator-declared Source this plugin projects into. It is
	// the namespace prefix a DerivedContract must stay inside (ADR-0047 §4).
	SourceName string
	// The allowlists the core registers ownership from and gates emissions against.
	FacetNamespaces []string
	LabelKeys       []string
	IdentitySchemes []string
}

// WriterRef is the Syncer writer identity the core would stamp on Provenance and
// the ownership registry — derived from the GRANT, never from the plugin.
//
// It is exposed here for one reason: so a plugin author can see the string their
// writes would be attributed to and confirm they cannot influence it (invariant
// #6). There is no setter, and that is the point.
func (g Grant) WriterRef() string {
	return "plugin/" + g.PluginIdentity + "/" + g.SourceName + "/syncer"
}

func (g Grant) allowsFacet(ns string) bool { return contains(g.FacetNamespaces, ns) }
func (g Grant) allowsLabel(k string) bool  { return contains(g.LabelKeys, k) }

// allowsIdentity applies BOTH gates: the scheme must be granted, AND a shared
// cross-source scheme additionally requires the trusted tier.
func (g Grant) allowsIdentity(scheme string) (bool, string) {
	if !contains(g.IdentitySchemes, scheme) {
		return false, "identity scheme not in operator grant"
	}
	if SharedIdentitySchemes[scheme] && g.Tier != TierTrusted {
		return false, "shared cross-source identity scheme requires the trusted tier"
	}
	return true, ""
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
