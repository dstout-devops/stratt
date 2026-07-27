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
// AGREEMENT is checked too, now that it can be. A ref carries an id AND a content hash, and port
// invariant #5 says schema drift is blocking — but until ADR-0138 D4 every plugin left `sha256`
// empty, because a plugin cannot hash a document that lives in the core binary. With self
// contracts shipping WITH the plugin, a pinned ref is a claim about the exact bytes the plugin will
// enforce, and core holds it against the bytes it validates Steps with.
//
// A plugin pins exactly what it OWNS. It cannot pin a seam — `capabilities/*`, or a neutrally-named
// contract like cert-issuer's that core keeps because more than one vendor may implement it — so
// those refs go out unpinned, meaning "no claim", and there is nothing to contradict. That falls
// straight out of the seam/self split rather than being a special case bolted onto it.

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
		for _, ref := range []struct{ role, id, sha string }{
			{"input", a.InputContract, a.InputSha},
			{"output", a.OutputContract, a.OutputSha}, // "" is lawful: an Action may return no typed outputs
		} {
			if ref.id == "" {
				continue
			}
			held, ok, err := contract.Get(ref.id)
			if err != nil {
				return fmt.Errorf("action %q: resolving advertised %s Contract %q: %w", name, ref.role, ref.id, err)
			}
			if !ok {
				return fmt.Errorf("action %q advertises %s Contract %q, which core does not hold — the plugin would be "+
					"checking args against a document core has never seen, so the two ends of the seam cannot be shown to "+
					"agree (§1.5: schema drift is blocking, never silently absorbed)", name, ref.role, ref.id)
			}
			// THE HASH CHECK (port invariant #5), and the loop this whole arc was opening. Until
			// ADR-0138 D4 a plugin could not pin a self contract at all — the document lived in the
			// core binary, so `sha256` went out empty and there was nothing to compare. Now the
			// document ships WITH the plugin, so a pinned ref is a claim about the exact bytes the
			// plugin will enforce, and core can hold it against the bytes it validates Steps with.
			//
			// UNPINNED IS LAWFUL and means "no claim": a plugin cannot hash a SEAM it does not own
			// (capabilities/*, or a neutrally-named contract like cert-issuer's, which core keeps
			// precisely because more than one vendor may implement it). Absent a claim there is
			// nothing to contradict. A claim that DISAGREES is drift, and drift is blocking.
			if ref.sha != "" && !strings.EqualFold(ref.sha, held.Hash) {
				return fmt.Errorf("action %q pins %s Contract %q to %s but core holds %s — the plugin would "+
					"conformance-check args against DIFFERENT BYTES than core validates the Step against, so a Step "+
					"could pass the load and fail at the plugin (or worse, pass both against divergent rules). "+
					"Schema drift is blocking, never silently absorbed (§1.5, port invariant #5)",
					name, ref.role, ref.id, short(ref.sha), short(held.Hash))
			}
		}
	}
	return nil
}

// short trims a hex digest for a diagnostic — the full 64 chars bury the message they are in.
func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
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
