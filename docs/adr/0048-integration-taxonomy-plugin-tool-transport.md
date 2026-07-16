# ADR 0048 — Where an integration lives: connector (plugin) vs migration (tool) vs transport (core port)

- **Status:** **Accepted** — a classification rule applying the already-accepted substrate model (ADR-0046/0047)
  to the recurring question "should system X be a plugin?". Not a charter evolution; it operationalizes
  content-blindness (§1.1/§0), the sovereign-contracts/multiple-transports discipline (§1.5), and the GPL/license
  boundary (§3, §1.4) for integration decisions.
- **Date:** 2026-07-16
- **Deciders:** Project steward (dstout)
- **Charter sections:** §0 (spine/plugins), §1.1 (content-blindness), §1.4 (boring spine, pluggable everything),
  §1.5 (sovereign contract, transports beneath), §1.6 (one capability, many transports), §3 (GPL boundary).
  Builds on ADR-0046 (substrate), ADR-0047 (port surface), ADR-0025 (awximport), ADR-0026 (awxfacade).
  Related: `docs/oss-connector-tool-landscape.md` (the license buckets).

## Context

Extracting the connector fleet behind the plugin port surfaced a recurring question — first as "if chef is a
plugin, should AWX be one, or do we ingest it from code?" Content-blindness (ADR-0046: external-system
expertise never lives in the reconcile spine) is the right test, but it does **not** resolve to a flat
plugin-vs-core binary. It resolves **three** ways depending on the *shape* of the work, and getting the shape
wrong is how a spine either re-accretes domain logic (`if awx {…}`) or wrongly plugin-ifies a transport.

## Decision

Classify every integration by **what shape the work is**, using two questions:

1. **Does it need to know what the external system *is*?** (content-blindness — ADR-0046)
2. **Is it a *runtime* relationship, a *one-shot* migration, or a *transport* onto our own capabilities?**

| Shape | Home | Why | Example |
|---|---|---|---|
| **Runtime external-SoR observation / action** (continuous: observe live state, converge, invoke) | **Plugin** (behind the port) | External-system expertise; a live relationship the reconcile engine drives | chef, vcenter, msgraph, awsec2, certissuer, salt |
| **One-shot migration** (enumerate a source once → transform into *our* desired-state vocabulary) | **Bounded tool** (not a port verb) | Still content-expertise, so out of the spine — but a one-shot transform emitting Views/Workflows is not a runtime port verb (ADR-0047: we do not mint verbs for one-shot shapes), and its output is *our* model | `awximport` (the "AWX exodus") |
| **Transport onto our own capabilities** (speak someone else's protocol so their clients drive *us*) | **Core port** (never a plugin) | It exposes *Stratt's* capabilities over another wire — §1.5 "transports beneath the sovereign contract", §1.6 "one capability, many transports" | `awxfacade` (`/api/v2`), OpenAPI, the MCP server |

The test in one line: **runtime relationship → plugin; one-shot exodus → tool; our-capability-over-their-wire →
core transport.** Content-blindness keeps the *spine* clean in all three; it does not by itself decide which of
the three a given piece is.

### The AWX worked example (the canonical case)
- **Observing/acting on a live AWX** → would be a plugin (like chef) — but the AWX we have is a *migration
  source*, not a live SoR we reconcile against, so this case does not currently exist.
- **`connectors/awx` + `awximport`** → the one-shot exodus: enumerate an AWX estate once, transform it into a
  Git-declared desired-state bundle (Views/Workflows/CredentialRefs/Contracts, §1.2 desired-state, never graph
  entities). A **bounded migration tool**. "Ingest from code" is correct *here* — it is not the `if awx {…}`
  anti-pattern because it is a self-contained transform emitting *our* vocabulary, not a branch in the reconcile
  spine. (Cleanup: `connectors/awx` is mis-filed — it is a migration enumerator, not a runtime Syncer, and
  belongs with `awximport`, not alongside chef/vcenter in `connectors/`.)
- **`awxfacade`** → a **core transport**, always. Making it a plugin would be a category error.

**So: no AWX plugin for the AWX we have.** The only future case that flips this is *continuous observation of a
live AWX during co-existence* — a runtime Syncer, which would then be a plugin exactly like chef.

### The license dimension (why the plugin boundary matters here)
`docs/oss-connector-tool-landscape.md` buckets integration targets by license. The **runtime-plugin** row is
also the **license firewall** (ADR-0047 invariant #13; §3): a plugin is its own module/binary/process, so it may
*orchestrate* a Bucket-2 copyleft tool (Ansible GPLv3, Proxmox AGPLv3, Ceph LGPL, …) by shelling out or calling
its stable API — the copyleft links only on the *plugin's* side of the wire, never in the Apache-2.0 core. This
is the structural generalization of the "Ansible is subprocess-only" carve-out. It reinforces the taxonomy: a
copyleft external system is a **plugin** target (orchestrate-only), never vendored into the spine, and never a
core transport.

## Consequences
- **Positive:** future asks ("should ServiceNow / Jamf / Proxmox / OpenStack be a plugin?") are decided by the
  table, once — not re-litigated each time. Runtime SoRs → plugins; one-time imports → tools; compat APIs → core
  transports. The license firewall falls out of the plugin boundary.
- **Cleanup surfaced:** relocate `connectors/awx` under the migration/exodus tooling (it is not a runtime
  Syncer); tracked, not done here.
- **Non-goal reminder:** a transport (façade) exposing Stratt's own capabilities is never a content-connector —
  keep those in core even when they speak a foreign protocol.

## Alternatives considered
- **"Everything external is a plugin"** — rejected: wrongly plugin-ifies transports (the AWX façade) and
  one-shot migrations, adding port/process overhead for work that isn't a runtime relationship.
- **"AWX is special, keep it all in core"** — rejected for the runtime case: AWX-API expertise is content-
  expertise like any other; only the *façade* (a transport) and the *one-shot importer* (a bounded tool) are
  legitimately core, and for reasons that generalize, not because AWX is special.
