package mockstratt

import (
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// The request half. ADR-0051's shape is that the EE-Job (subprocess) transport and
// the gRPC transport send the SAME ApplyRequest — "one request contract serves both
// transports". That is reproduced here rather than approximated: both transports in
// this package call ApplyRequest below, so a plugin author who switches transports
// is genuinely switching only the transport.

// Request is what the core would hand a plugin for one Apply. Every field is a
// thing the CORE resolved; none of it is negotiable by the plugin.
type Request struct {
	// Params is the opaque desired-state payload (§1.1). The core never parses it —
	// and neither does this package, which is why it is []byte and not a map.
	Params []byte
	// Targets is the core-resolved actuation set, crossing LEGIBLY and never baked
	// into Params (ADR-0047 §1.1). It carries blast-radius weight, it is what §1.8
	// descent renders, and it is the key the confused-deputy gate matches on.
	Targets []ApplyTarget
	// DryRun is the check-mode bit (MF6). The core refuses it for an Actuator that
	// did not declare dryRunnable — a shim silently ignoring it would run live side
	// effects — so this package refuses it the same way (see Subprocess.DryRunnable).
	DryRun bool
	// Principal is the asserted acting identity. The plugin trusts it only because
	// the call provably arrived over the authenticated core→plugin channel
	// (invariant #3).
	Principal string
	// Creds are CredentialRef NAMES, optionally with resolved COORDINATES (ADR-0052).
	// There is no material field here and there never will be (§2.5) — the plugin
	// resolves against its own broker at point of use.
	Creds []Credential
	// Capabilities are core-resolved capability handles keyed by class (ADR-0105) —
	// a legible, core-authored channel, never baked into Params (§1.5/§1.8).
	Capabilities map[string]*pluginv1.CapabilityHandle
	// Content is the Actuator's declared content root, relative path -> bytes, which
	// the core mounts at project/ (ADR-0134 D2). Subprocess-transport only: it is
	// mounted, not sent, precisely because the core copies a directory a DECLARATION
	// named and learns nothing about what is in it.
	Content map[string]string
	// ResumeToken continues a checkpointed Apply (invariant #7); "" == fresh.
	ResumeToken string
}

// Credential is a use-checked CredentialRef: a name, plus optionally the backend
// COORDINATES the SDK broker resolves against (ADR-0052). Names and paths only —
// there is deliberately no material field, ever.
type Credential struct {
	Name string
	// Resolved carries the backend coordinates on the local/trusted path. Left nil
	// on a relay path (MF-C), where a plugin gets the name alone and must fail
	// closed — a case worth testing, which is why it is a pointer and not a value.
	Resolved *pluginv1.ResolvedRef
}

// ApplyRequest renders the Request as the sovereign wire message. This is the
// single place either transport builds one.
func (r Request) ApplyRequest() *pluginv1.ApplyRequest {
	principal := r.Principal
	if principal == "" {
		principal = "mockstratt"
	}
	creds := make([]*pluginv1.CredentialRef, 0, len(r.Creds))
	for _, c := range r.Creds {
		creds = append(creds, &pluginv1.CredentialRef{Name: c.Name, Resolved: c.Resolved})
	}
	targets := make([]*pluginv1.ApplyTarget, 0, len(r.Targets))
	for _, t := range r.Targets {
		hops := make([]*pluginv1.JumpHop, 0, len(t.Jump))
		for _, h := range t.Jump {
			hops = append(hops, &pluginv1.JumpHop{Name: h.Name, Address: h.Address, Port: h.Port})
		}
		ids := t.IdentityKeys
		if ids == nil {
			// The core always sends at least one identity key, because per-target
			// write-back has to re-correlate to the resolved Entity (§1.2). Defaulting
			// it here keeps a terse test target realistic rather than letting a plugin
			// develop against a shape the core never sends.
			ids = map[string]string{"host.name": t.Name}
		}
		targets = append(targets, &pluginv1.ApplyTarget{
			Name: t.Name, Address: t.Address, Port: t.Port,
			Vars: t.Vars, IdentityKeys: ids, Jump: hops,
		})
	}
	return &pluginv1.ApplyRequest{
		Envelope: &pluginv1.Envelope{
			Principal: &pluginv1.Principal{Id: principal, Kind: "user"},
			Creds:     creds,
		},
		Desired:              &pluginv1.Payload{Bytes: r.Params},
		DryRun:               r.DryRun,
		Targets:              targets,
		ResumeToken:          r.ResumeToken,
		ResolvedCapabilities: r.Capabilities,
	}
}
