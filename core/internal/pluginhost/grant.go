package pluginhost

import "github.com/dstout-devops/stratt/types"

// Tier is a plugin's trust tier, established at registration from its signature
// (community binaries are unsigned/self-serve; trusted are first-party/signed).
// Tier gates the most dangerous capability — cross-source identity emission
// (ADR-0046 finding #4).
type Tier string

const (
	TierCommunity Tier = "community"
	TierTrusted   Tier = "trusted"
)

// SharedIdentitySchemes are cross-source correlation schemes: a value under one
// of these can merge Entities observed by DIFFERENT Sources (e.g. a vCenter VM
// and a Chef node that share a DNS name). Emitting one is estate-wide power, so
// a COMMUNITY-tier plugin may never emit them — even if the operator grant lists
// the scheme (defence-in-depth: tier + grant, ADR-0046 finding #4). Vendor-
// namespaced schemes (vcenter.uuid, aws.instanceId) are source-local and safe.
var SharedIdentitySchemes = map[string]bool{
	"dns.fqdn": true,
	"mac":      true,
}

// Grant is the operator-declared (Config-as-Code) authority for one plugin,
// keyed on its authenticated channel identity. It is the single source of TRUTH
// for ownership, the Source binding, and which identity schemes the plugin may
// emit (ADR-0046 findings #1/#4/#6). The plugin's Manifest is a REQUEST that
// must MATCH this grant — a plugin never self-grants ownership, so connection
// order can never decide who owns a namespace (§2.4, no implicit precedence).
type Grant struct {
	// PluginIdentity is the authenticated channel identity; the Manifest's
	// plugin_id must equal it or registration fails.
	PluginIdentity string
	Tier           Tier
	// Source is operator-declared: Kind, Name, Endpoint, CredentialRef. The
	// plugin never invents a Source; home Cell comes from the registering daemon
	// (ADR-0044/0045).
	Source types.Source
	// FacetNamespaces / LabelKeys / IdentitySchemes are the allowlists the core
	// registers ownership from and gates emissions against.
	FacetNamespaces []string
	LabelKeys       []string
	IdentitySchemes []string
	// TombstoneSchemes are the identity schemes the host tombstones by on a
	// full-sync boundary (ADR-0042). Must be a subset of IdentitySchemes.
	TombstoneSchemes []string
}

// WriterRef is this plugin's Syncer writer identity for Provenance and the
// facet/label ownership registry — derived from the grant, never the plugin.
func (g Grant) WriterRef() string {
	return "plugin/" + g.PluginIdentity + "/" + g.Source.Name + "/syncer"
}

func (g Grant) allowsFacet(ns string) bool { return contains(g.FacetNamespaces, ns) }
func (g Grant) allowsLabel(k string) bool  { return contains(g.LabelKeys, k) }

// allowsIdentity applies BOTH gates (tier + grant): the scheme must be granted,
// AND a shared cross-source scheme additionally requires the trusted tier.
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
