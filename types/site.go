package types

// Site is a remote execution locus — a satellite dispatcher reachable over a
// NATS leaf node (charter §2.3, §3; the Receptor replacement). A Run's targets
// route to a Site by their location (the mgmt.site Facet); the built-in local
// Site is the central cluster and needs no declaration. A Site is a
// CaC-declared projection (§1.2): the declaration lives in Git; live up/down
// status is ephemeral and kept in NATS KV, never written to the graph.
type Site struct {
	// Name is the stable reference used in mgmt.site Facets, dispatch subjects
	// (stratt.dispatch.<name>), and Run provenance. Never "local" — that name
	// is reserved for the built-in central locus.
	Name string `json:"name"`
	// Mode is how work reaches the Site's agent:
	//   push — the hub dispatches JobSpecs over NATS to a connected agent.
	//   pull — an egress-only agent pulls signed Bundles from an OCI registry.
	Mode string `json:"mode"`
	// Namespace is the K8s namespace the Site's agent runs Jobs in (audit only
	// on the hub — the agent owns its own clientset).
	Namespace string `json:"namespace,omitempty"`
	// Description is free-form operator context (region, data-locality note).
	Description string `json:"description,omitempty"`
	// DeclaredBy records the declaration path: "cac" (Git desired state, §1.2)
	// or "api". Mirrors View/Trigger/Emitter.
	DeclaredBy string `json:"declaredBy,omitempty"`
}

// Site modes.
const (
	SiteModePush = "push"
	SiteModePull = "pull"
)

// LocalSite is the built-in central execution locus (today's hub cluster). A
// target whose mgmt.site Facet is unset routes here, and this Site is never
// declared, never dispatched over NATS, and never appears in graph.site.
const LocalSite = "local"
