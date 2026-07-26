package connectorregistry

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dstout-devops/stratt/core/internal/contract"
)

// ── declared actionNames vs the advertised Manifest (ADR-0138 verification half) ──
//
// `provides` has always been VERIFIED against the Manifest: an operator may declare a capability,
// but a provider whose plugin does not advertise it is a phantom and does not count (§1.5,
// ADR-0104 D1). `actionNames` had no such check. The estate said "this plugin exposes
// helm/deploy", core registered that name into the dispatch table, and nothing ever asked the
// plugin whether it ships the Action. A typo, a removed Action, or a plugin rolled back to a
// version that no longer has it all produce the same thing: a dispatchable name that fails at
// Invoke — the failure landing in a Run, not in a diff (§1.8).
//
// This closes it with the SAME verdict `provides` already gets, and adds the ID-level half of
// the Contract cross-check: the ids a plugin advertises must name documents core holds.
//
// SCOPE (deliberate). This checks the DECLARED actionNames, not every Action the plugin
// advertises. vcenter advertises seventeen; an estate registers the ones it wants dispatchable.
// The declared set is exactly the set where core will validate a Step's params, so it is the set
// where a disagreement is reachable. An advertised-but-unregistered Action naming an unresolvable
// Contract is real drift, but not a seam this floor can hit — flagging it would refuse to enable
// a working Actuator over an Action nobody invokes.
//
// NOT YET CHECKED — the ids are cross-checked for EXISTENCE, not for AGREEMENT. Core validates a
// generic `action: X` Step against `actions/X.input` (its own convention); the plugin advertises
// whichever id it conformance-checks against. For most Actions those coincide. For a
// capability-implementing Action they deliberately DIVERGE: netbox/ipam-resolve advertises
// `capabilities/ipam.input` because the class contract is what a capability call is validated
// against (ADR-0112 D2), while `actions/netbox/ipam-resolve.input` also exists for the explicit-Step
// path, the two bound only by a hand-written per-plugin co-fidelity test. Core cannot today tell a
// capability implementation from a generic Action, so it cannot know which divergences are lawful —
// that is precisely the fact ADR-0140 D1 makes DECLARED (`ActionDecl.implements`). Hash-equality
// (port invariant #5) needs the document to ship with the plugin, which is ADR-0138 D4's relocation.
// Both are gated on work this check is the precondition for.

// verifyDeclaredActions fetches addr's Manifest and checks the declared actionNames against it.
// A no-op when nothing is declared — most Actuators expose no targetless Action, and they must
// not pay a Manifest round-trip to enable.
//
// A fetch failure REFUSES the enable rather than admitting the declaration unverified. That is
// the opposite of the health-independence rule provider verification follows (a transient blip
// must never drop an already-confirmed provider, §2.4/D3), and deliberately so: verification
// resolves a BINDING between declarations and must not let liveness decide it, whereas enable
// already fails closed on the dial immediately above this. An unreachable plugin has nothing to
// register anyway.
func (r *Registry) verifyDeclaredActions(ctx context.Context, addr string, declared []string) error {
	if len(declared) == 0 {
		return nil
	}
	m, err := r.manifest(ctx, addr)
	if err != nil {
		return fmt.Errorf("manifest fetch for actionNames verification: %w", err)
	}
	return checkAdvertisedActions(declared, m)
}

// checkAdvertisedActions verifies a declaration's actionNames against the plugin's Manifest.
// Returns a diagnostic naming the specific disagreement, or nil.
func checkAdvertisedActions(declared []string, m PluginManifest) error {
	advertised := make(map[string]AdvertisedAction, len(m.Actions))
	for _, a := range m.Actions {
		advertised[a.Name] = a
	}
	for _, name := range declared {
		a, ok := advertised[name]
		if !ok {
			return fmt.Errorf("action %q is declared in actionNames but the plugin's Manifest advertises no such Action "+
				"(advertised: %s) — the estate grants; the Manifest is the plugin's own account of what it ships, and "+
				"registering a name the plugin never claimed makes the failure land in a Run instead of a diff (§1.5, §1.8)",
				name, advertisedList(m))
		}
		// An Action with no input Contract is an uncontracted operation surface (§2.2,
		// ADR-0031) — the same rule the estate loader applies from core's side, applied
		// here to the plugin's own advertisement.
		if a.InputContract == "" {
			return fmt.Errorf("action %q advertises no input Contract — an uncontracted operation must not exist (§2.2, ADR-0031)", name)
		}
		for _, ref := range []struct{ role, id string }{
			{"input", a.InputContract},
			{"output", a.OutputContract}, // "" is lawful: an Action may return no typed outputs
		} {
			if ref.id == "" {
				continue
			}
			if _, ok, err := contract.Get(ref.id); err != nil {
				return fmt.Errorf("action %q: resolving advertised %s Contract %q: %w", name, ref.role, ref.id, err)
			} else if !ok {
				return fmt.Errorf("action %q advertises %s Contract %q, which core does not hold — the plugin would be "+
					"checking args against a document core has never seen, so the two ends of the seam cannot be shown to "+
					"agree (§1.5: schema drift is blocking, never silently absorbed)", name, ref.role, ref.id)
			}
		}
	}
	return nil
}

// advertisedList renders the advertised Action names for a diagnostic — the descent pointer
// (§1.8): "no such Action" is only actionable next to what the plugin DID advertise.
func advertisedList(m PluginManifest) string {
	if len(m.Actions) == 0 {
		return "none"
	}
	names := make([]string, 0, len(m.Actions))
	for _, a := range m.Actions {
		names = append(names, a.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
