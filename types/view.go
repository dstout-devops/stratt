package types

import "encoding/json"

// View is a saved, versioned, CaC-declared graph query producing a live
// Entity set (charter §2.1). It unifies what other tools call inventory,
// smart/constructed inventory, Jamf Smart Groups, and SCCM collections.
// Views referenced by Assignments are Git-only — a View edit is a
// blast-radius change (§4.3, §5.4).
type View struct {
	// Name is the stable reference, addressed as view://<name>
	// (e.g. view://retail/kiosk-devices, charter §4.2).
	Name string `json:"name"`
	// Version increments with every declared change; Assignments and Runs
	// record the version they resolved against.
	Version int64 `json:"version"`
	// Selector is the query document. It is structured selector DATA
	// (kind/label/facet predicates) — deliberately not an expression
	// language (non-goal: no new configuration languages, charter §1).
	Selector ViewSelector `json:"selector"`
	// DeclaredBy records which declaration path owns this View: "cac" for
	// the Git-declared desired state (§1.2), "api" for direct declaration.
	// CaC may adopt an api View; the api path may never modify a cac View.
	DeclaredBy string `json:"declaredBy,omitempty"`
}

// ViewSelector is the structured query a View declares. All present clauses
// must match (conjunction). The charter's shorthand view://label:run=X
// (§5.1) desugars to a Labels-only selector.
type ViewSelector struct {
	// Kinds matches Entities whose Kind is any of these (empty = any kind).
	Kinds []string `json:"kinds,omitempty"`
	// Labels matches Entities carrying every listed label key=value.
	Labels map[string]string `json:"labels,omitempty"`
	// Facets matches on Facet values by namespace and JSON path equality,
	// e.g. {namespace: "os.kernel", path: "family", equals: "linux"}.
	Facets []FacetPredicate `json:"facets,omitempty"`
}

// FacetPredicate matches one value inside a Facet document.
type FacetPredicate struct {
	Namespace string `json:"namespace"`
	// Path is a dotted path within the Facet value ("" = whole value).
	Path string `json:"path,omitempty"`
	// Equals is the JSON value the addressed field must equal.
	Equals json.RawMessage `json:"equals"`
}
