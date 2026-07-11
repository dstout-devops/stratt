package types

// Source is an external system of record, registered with CredentialRefs and
// trust settings (charter §2.2). Sources stay authoritative — the graph only
// ever holds a rebuildable projection of them (§1.2).
type Source struct {
	ID string `json:"id"`
	// Kind names the class of system (e.g. "vcenter").
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Endpoint is the connection locator (URL/host). Secret material is
	// never stored here — only a CredentialRef pointer (§2.5).
	Endpoint      string `json:"endpoint"`
	CredentialRef string `json:"credentialRef,omitempty"`
}
