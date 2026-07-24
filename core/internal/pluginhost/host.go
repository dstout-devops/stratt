// Package pluginhost is the core-side host of a Syncer-class plugin over the
// sovereign plugin port (ADR-0046 Phase B). It is the SOLE graph writer for the
// plugin's Source: the plugin holds no DB path, so single-writer + provenance
// survive the wire (enforce_write_path, ADR-0044/0045). The host governs on the
// operator Grant, not on anything the plugin asserts:
//
//   - Ownership (§2.1) is registered from the GRANT, never the Manifest; the
//     Manifest must MATCH the grant or registration fails (finding #1).
//   - Provenance (#6) is stamped from the channel identity; an ObservedEntity
//     carries no principal, so a plugin cannot claim it.
//   - Identity-scheme emission is gated by tier + grant (finding #4); ungranted
//     or shared-from-community schemes are dropped, never written.
//   - Facet VALUES are validated against the pinned schema at the write path
//     (contract.ValidateFacet inside UpsertEntities); the host never lets the
//     plugin introduce a schema (finding #2).
package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/planstore"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// Host drives one Syncer plugin against the graph under one operator Grant.
type Host struct {
	store  *graph.Store
	client pluginv1.PluginServiceClient
	grant  Grant
	log    *slog.Logger

	source     types.Source
	rejections []Rejection
	plans      *planstore.Store // set for Actuator hosts (the Plan verb); nil otherwise

	// credCoordinates gates SecretBroker ResolvedRef enrichment (ADR-0052 MF-C): the
	// host attaches use-checked Secret COORDINATES to the Envelope only on the LOCAL/
	// trusted path. A relay-backed host (a plugin at an untrusted Site) sets this
	// false, so hub Secret coordinates never cross the relay — fail-closed. New()
	// defaults it TRUE (a hub-local host); WithoutCredentialCoordinates() disables it.
	credCoordinates bool
}

// UsePlanStore attaches the content-addressed plan store an Actuator host needs
// for the Plan verb (ADR-0047 §8) — the core content-addresses + encrypts the
// saved plan and re-hashes it at the Apply boundary. Syncer/Action hosts leave it
// nil. Returns the host for chaining at wiring time.
func (h *Host) UsePlanStore(p *planstore.Store) *Host { h.plans = p; return h }

// WithoutCredentialCoordinates disables SecretBroker ResolvedRef enrichment for this
// host (ADR-0052 MF-C) — used for a RELAY-backed host so a plugin at an untrusted
// Site never learns hub Secret coordinates. Returns the host for chaining.
func (h *Host) WithoutCredentialCoordinates() *Host { h.credCoordinates = false; return h }

// Credential is a use-checked CredentialRef with its resolved Secret COORDINATES
// (ADR-0052) — names/paths only, NEVER material. The host attaches these to the
// Envelope (as a ResolvedRef) on the local path so the plugin's SDK SecretBroker can
// resolve the material itself; withheld on a relay host (MF-C).
type Credential struct {
	RefName         string
	SecretNamespace string
	SecretName      string
	// Vault, when non-nil, is a backend: vault KV coordinate (ADR-0094) rendered
	// INSTEAD OF the SecretName pair. The plugin's SDK SecretBroker reads the KV
	// secret itself, as itself (§2.5) — the core carries only the coordinate.
	Vault *types.VaultLocator
	Keys  []CredentialKey
}

// CredentialKey mirrors one CredentialRef Injection entry: the Secret DATA key the
// plugin may read + the logical name/mode the pod path would have projected.
type CredentialKey struct{ Key, As, Name string }

// wireCred renders one authorized Credential onto the Envelope. The ResolvedRef
// coordinates ride ONLY when this host allows them (MF-C); otherwise the plugin gets
// the name alone and must fail closed. No material field exists to leak (§2.5).
func (h *Host) wireCred(c Credential) *pluginv1.CredentialRef {
	ref := &pluginv1.CredentialRef{Name: c.RefName}
	if !h.credCoordinates {
		return ref
	}
	keys := make([]*pluginv1.ResolvedKey, 0, len(c.Keys))
	for _, k := range c.Keys {
		keys = append(keys, &pluginv1.ResolvedKey{Key: k.Key, As: k.As, Name: k.Name})
	}
	resolved := &pluginv1.ResolvedRef{Keys: keys}
	if c.Vault != nil {
		// backend: vault (ADR-0094) — KV coordinates INSTEAD OF the K8s Secret pair.
		resolved.Vault = &pluginv1.VaultCoords{Mount: c.Vault.Mount, Path: c.Vault.Path, KvV2: c.Vault.KVv2}
	} else {
		resolved.SecretNamespace = c.SecretNamespace
		resolved.SecretName = c.SecretName
	}
	ref.Resolved = resolved
	return ref
}

// Rejection is a governance refusal the host surfaces (an ungranted/land-grabbed
// emission it dropped). Persisting these as graph Findings (§1.8) is the
// follow-up; for now they are logged and retained for observability/tests.
type Rejection struct {
	Kind   string // "identity-scheme" | "label" | "facet" | "entity" | "relation" | "provisioned-cred"
	Detail string // the offending scheme/key/namespace
	Reason string
}

// InvokeOutcome is what an Action invocation returns to its calling Run Step: the
// typed outputs (for cross-Step binding, ADR-0031), the graph ids of entities it
// provisioned (provision→configure, ADR-0017), and the CredentialRef names it
// provisioned (§2.5, namespace-confined).
type InvokeOutcome struct {
	OK                bool
	Outputs           []byte
	OutputContract    string
	ProvisionedEntity []string
	ProvisionedCreds  []string
	Events            int
}

func New(store *graph.Store, client pluginv1.PluginServiceClient, grant Grant, log *slog.Logger) *Host {
	return &Host{store: store, client: client, grant: grant, credCoordinates: true,
		log: log.With("plugin", grant.PluginIdentity, "source", grant.Source.Name)}
}

// Rejections returns the governance refusals recorded so far (test/observability).
func (h *Host) Rejections() []Rejection { return h.rejections }

func (h *Host) reject(kind, detail, reason string) {
	h.rejections = append(h.rejections, Rejection{Kind: kind, Detail: detail, Reason: reason})
	h.log.Warn("plugin emission rejected", "kind", kind, "detail", detail, "reason", reason)
}

// Register validates the plugin's Manifest against the operator grant, then
// registers the Source and the §2.1 ownership FROM THE GRANT. A Manifest that
// requests anything outside the grant fails registration (blocking) — the plugin
// never syncs. The Manifest is advertisement; the grant is truth.
func (h *Host) Register(ctx context.Context) error {
	resp, err := h.client.GetManifest(ctx, &pluginv1.GetManifestRequest{})
	if err != nil {
		return fmt.Errorf("pluginhost: get manifest: %w", err)
	}
	m := resp.GetManifest()

	// Identity binding: the asserted plugin_id must equal the authenticated
	// channel identity the grant is keyed on. (Over insecure bufconn this is the
	// stand-in; in production the channel is mTLS/token and binds it — inv #3.)
	if m.GetPluginId() != h.grant.PluginIdentity {
		return fmt.Errorf("pluginhost: manifest identity %q != granted identity %q", m.GetPluginId(), h.grant.PluginIdentity)
	}
	// Gate on the OBSERVE verb, not a singular Class: a full-featured plugin may be
	// BOTH an Actuator and a Syncer (e.g. Crossplane BUILDS its Claims and OBSERVES
	// their as-built state as a registered Source). Consistent with the Actuator and
	// Emitter paths, which gate on identity/verb and never on Class — "the Manifest is
	// advertisement; the grant is truth" (ADR-0046). The Class field remains the
	// plugin's declared primary kind; Verbs is the authoritative capability surface.
	if !manifestHasVerb(m, pluginv1.Verb_VERB_OBSERVE) {
		return fmt.Errorf("pluginhost: plugin %q cannot Observe (verbs %v) — a Syncer grant needs the OBSERVE verb", h.grant.PluginIdentity, m.GetVerbs())
	}

	// Every REQUESTED facet namespace must be granted. The sha256 of a declared
	// contract is checked against the core's pinned schema at the write path
	// (contract.ValidateFacet); manifest-time sha256 equality is a follow-up once
	// the contract registry exposes schema hashes.
	for _, c := range m.GetContracts() {
		if !h.grant.allowsFacet(c.GetSchemaId()) {
			return fmt.Errorf("pluginhost: plugin requests unowned facet namespace %q (not in grant)", c.GetSchemaId())
		}
	}
	// Every requested tombstone scheme must pass the identity gate (tier+grant).
	for _, ts := range m.GetTombstoneSchemes() {
		if ok, reason := h.grant.allowsIdentity(ts); !ok {
			return fmt.Errorf("pluginhost: tombstone scheme %q rejected: %s", ts, reason)
		}
	}

	// Register the Source from the GRANT (operator-declared endpoint/credRef);
	// homes to this daemon's Cell (ADR-0044). Ownership follows from the grant.
	src, err := h.store.RegisterSource(ctx, h.grant.Source)
	if err != nil {
		return fmt.Errorf("pluginhost: register source: %w", err)
	}
	h.source = src

	ref := h.grant.WriterRef()
	for _, ns := range h.grant.FacetNamespaces {
		authoritative := contains(h.grant.AuthoritativeFacetNamespaces, ns)
		if err := h.store.RegisterFacetOwner(ctx, types.FacetOwner{Namespace: ns, OwnerKind: "syncer", OwnerRef: ref, Authoritative: authoritative}); err != nil {
			return fmt.Errorf("pluginhost: register facet owner %q: %w", ns, err)
		}
	}
	for _, k := range h.grant.LabelKeys {
		if err := h.store.RegisterLabelOwner(ctx, types.LabelOwner{Key: k, OwnerKind: "syncer", OwnerRef: ref}); err != nil {
			return fmt.Errorf("pluginhost: register label owner %q: %w", k, err)
		}
	}
	h.log.Info("registered", "source_id", src.ID, "facets", h.grant.FacetNamespaces, "tier", h.grant.Tier)
	return nil
}

// Deregister reverses Register's ownership claims (ADR-0103 Connector disable): it releases
// the facet/label ownership grant rows this host registered, keyed by the grant's WriterRef
// so it never revokes another source's claim. It deliberately does NOT delete the Source row
// or tombstone Entities — that placement/lifecycle is the home-gate/re-home single writer's
// domain (§2.4); the Source projection is rebuildable (§1.2). Idempotent — safe to call on a
// host that never registered, or twice.
func (h *Host) Deregister(ctx context.Context) error {
	ref := h.grant.WriterRef()
	for _, ns := range h.grant.FacetNamespaces {
		if err := h.store.DeregisterFacetOwner(ctx, ns, ref); err != nil {
			return fmt.Errorf("pluginhost: deregister facet owner %q: %w", ns, err)
		}
	}
	for _, k := range h.grant.LabelKeys {
		if err := h.store.DeregisterLabelOwner(ctx, k, ref); err != nil {
			return fmt.Errorf("pluginhost: deregister label owner %q: %w", k, err)
		}
	}
	h.log.Info("deregistered", "source", h.grant.Source.Name, "facets", h.grant.FacetNamespaces)
	return nil
}

// ValidateManifest fetches the plugin's Manifest and checks its asserted identity
// against the hub-held grant (ADR-0049 F1) — the anti-spoof binding for a RELAYED
// Site plugin, where manifest.plugin_id is a string the Site controls and a
// compromised agent could relay a different plugin. Governance stays hub-side: a
// mismatch is rejected BEFORE any verb runs. Lighter than Register (no Source/owner
// registration, no Syncer-class assumption) so it runs per-Run on a Site host. The
// deeper end-to-end auth (a plugin-held key the agent cannot forge) is the tracked
// hardening; until then the trust model is bounded to the grant's delegated scope.
func (h *Host) ValidateManifest(ctx context.Context) error {
	resp, err := h.client.GetManifest(ctx, &pluginv1.GetManifestRequest{})
	if err != nil {
		return fmt.Errorf("pluginhost: get manifest: %w", err)
	}
	if id := resp.GetManifest().GetPluginId(); id != h.grant.PluginIdentity {
		return fmt.Errorf("pluginhost: manifest identity %q != granted identity %q (anti-spoof, ADR-0049 F1)", id, h.grant.PluginIdentity)
	}
	return nil
}

// SyncLoop re-runs a full Sync every interval until ctx ends. A transient sync
// error is logged and retried on the next tick — the full sync is the recovery
// mechanism (ADR-0046: reconcile-as-recovery). Register must have run first;
// homeSupervise calls it as the register step before this loop.
func (h *Host) SyncLoop(ctx context.Context, interval time.Duration) error {
	for {
		if err := h.Sync(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			h.log.Warn("full sync failed; retrying next tick", "error", err)
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// manifestHasVerb reports whether the plugin advertises a verb — the capability
// gate for a full-featured (multi-verb) plugin registering under one verb's grant.
func manifestHasVerb(m *pluginv1.Manifest, v pluginv1.Verb) bool {
	for _, got := range m.GetVerbs() {
		if got == v {
			return true
		}
	}
	return false
}

func (h *Host) provenance() types.Provenance {
	return types.Provenance{
		WriterKind: types.WriterSyncer,
		WriterRef:  h.grant.WriterRef(),
		SourceID:   h.source.ID,
		At:         time.Now().UTC(),
	}
}

// Sync runs one Observe pass: it projects each ObservedEntity (gating identity
// schemes + labels, validating facets at the write path) and, on the full-sync
// boundary, tombstones everything absent for each granted tombstone scheme
// (ADR-0042). The host is the only writer; the plugin proposed values, nothing
// more.
func (h *Host) Sync(ctx context.Context) error {
	if h.source.ID == "" {
		return errors.New("pluginhost: Sync before Register")
	}
	// Resume from the persisted delta cursor (e.g. msgraph @odata.deltaLink); "" is
	// a full sync. The HOST owns the cursor now — the plugin holds no store, so
	// single-writer/provenance stay core-side (ADR-0046/0047).
	cursor, _ := h.store.SyncCursor(ctx, h.source.ID)
	stream, err := h.client.Observe(ctx, &pluginv1.ObserveRequest{Cursor: cursor})
	if err != nil {
		return fmt.Errorf("pluginhost: observe: %w", err)
	}
	prov := h.provenance()
	projector := h.store.NormalizerProjector()
	seen := map[string][]string{}        // tombstone scheme -> seen values this full sync
	seenRels := map[string][][2]string{} // relType -> [ [fromID, toID], … ] this full sync (ADR-0081 MF-2)

	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break // clean end of stream
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("pluginhost: observe recv: %w", err)
		}

		type pendingRel struct {
			fromID string
			rel    *pluginv1.ObservedRelation
		}
		var pending []pendingRel

		for _, e := range resp.GetEntities() {
			up, ok := h.toUpsert(e)
			if !ok {
				continue
			}
			ids, err := projector.UpsertEntities(ctx, prov, []graph.EntityUpsert{up})
			if errors.Is(err, graph.ErrIdentityConflict) {
				h.reject("entity", up.Kind, "identity keys match multiple entities; not merging (§1.2)")
				continue
			}
			if err != nil {
				return fmt.Errorf("pluginhost: upsert: %w", err)
			}
			for _, rel := range e.GetRelations() {
				pending = append(pending, pendingRel{fromID: ids[0], rel: rel})
			}
			for _, s := range h.grant.TombstoneSchemes {
				if v, ok := up.IdentityKeys[s]; ok {
					seen[s] = append(seen[s], v)
				}
			}
		}

		// Relations resolve AFTER all entities in the window are present, targeting
		// BY IDENTITY (ADR-0047 §1): the target scheme is tier+grant gated exactly
		// as an emitted identity key is, and an unresolved target drops the edge
		// with a rejection — NEVER a vivified placeholder Entity.
		for _, pr := range pending {
			rel := pr.rel
			if ok, reason := h.grant.allowsIdentity(rel.GetToScheme()); !ok {
				h.reject("relation-target", rel.GetToScheme(), reason)
				continue
			}
			toID, found, err := h.store.EntityIDByIdentity(ctx, rel.GetToScheme(), rel.GetToValue())
			if err != nil {
				return fmt.Errorf("pluginhost: resolve relation target: %w", err)
			}
			if !found {
				h.reject("relation", rel.GetType(), "target "+rel.GetToScheme()+"="+rel.GetToValue()+" not found; edge dropped (no vivify)")
				continue
			}
			if err := projector.UpsertRelation(ctx, prov, rel.GetType(), pr.fromID, toID); err != nil {
				return fmt.Errorf("pluginhost: upsert relation: %w", err)
			}
			seenRels[rel.GetType()] = append(seenRels[rel.GetType()], [2]string{pr.fromID, toID})
		}

		for _, g := range resp.GetGone() {
			if ok, reason := h.grant.allowsIdentity(g.GetScheme()); !ok {
				h.reject("identity-scheme", g.GetScheme(), "gone: "+reason)
				continue
			}
			if _, err := projector.TombstoneByIdentity(ctx, prov, g.GetScheme(), g.GetValue()); err != nil {
				return fmt.Errorf("pluginhost: tombstone gone: %w", err)
			}
		}

		// GoneRelations: a delta feed's retracted edges (ADR-0059 decision 7a — Syncer
		// delta retraction). Both endpoints are resolved BY IDENTITY and tier+grant
		// gated exactly as a relation TARGET is; an already-gone endpoint means the
		// endpoint-tombstone cascade (7b) already retracted the edge, so it is a no-op.
		for _, gr := range resp.GetGoneRelations() {
			if ok, reason := h.grant.allowsIdentity(gr.GetFromScheme()); !ok {
				h.reject("relation-gone-from", gr.GetFromScheme(), reason)
				continue
			}
			if ok, reason := h.grant.allowsIdentity(gr.GetToScheme()); !ok {
				h.reject("relation-gone-to", gr.GetToScheme(), reason)
				continue
			}
			fromID, ffound, err := h.store.EntityIDByIdentity(ctx, gr.GetFromScheme(), gr.GetFromValue())
			if err != nil {
				return fmt.Errorf("pluginhost: resolve gone-relation from: %w", err)
			}
			toID, tfound, err := h.store.EntityIDByIdentity(ctx, gr.GetToScheme(), gr.GetToValue())
			if err != nil {
				return fmt.Errorf("pluginhost: resolve gone-relation to: %w", err)
			}
			if !ffound || !tfound {
				continue // an endpoint already gone → its cascade retracted the edge
			}
			if err := projector.RetractRelation(ctx, gr.GetType(), fromID, toID); err != nil {
				return fmt.Errorf("pluginhost: retract gone relation: %w", err)
			}
		}

		if resp.GetFullSyncComplete() {
			for _, s := range h.grant.TombstoneSchemes {
				if _, err := projector.TombstoneAbsent(ctx, prov, s, seen[s]); err != nil {
					return fmt.Errorf("pluginhost: tombstone absent %q: %w", s, err)
				}
			}
			// Per-source full-sync delete-and-replace of this Source's OWN observed
			// edges (ADR-0081 MF-2): a reparented target — an edge whose endpoints both
			// still exist but that was not re-emitted — is retracted here, which neither
			// GoneRelations (a full sync sends none) nor the endpoint-tombstone cascade
			// (both endpoints live) would collect. Sweeps every relation type the Source
			// emitted this cycle OR previously owned (so a type it stopped emitting
			// entirely is also cleared). Scoped to this Source — never another's edges.
			relTypes, err := h.store.RelationTypesBySource(ctx, h.source.ID)
			if err != nil {
				return fmt.Errorf("pluginhost: relation types: %w", err)
			}
			typeSet := map[string]struct{}{}
			for _, t := range relTypes {
				typeSet[t] = struct{}{}
			}
			for t := range seenRels {
				typeSet[t] = struct{}{}
			}
			for t := range typeSet {
				kf := make([]string, 0, len(seenRels[t]))
				kt := make([]string, 0, len(seenRels[t]))
				for _, pair := range seenRels[t] {
					kf = append(kf, pair[0])
					kt = append(kt, pair[1])
				}
				if _, err := projector.RetractSourceRelationPresenceExcept(ctx, h.source.ID, t, kf, kt); err != nil {
					return fmt.Errorf("pluginhost: replace source relations %q: %w", t, err)
				}
			}
		}

		// Persist the delta resume token so the next SyncLoop tick continues the
		// feed instead of re-enumerating (ADR-0042 cursor semantics, host-owned).
		if nc := resp.GetNextCursor(); nc != "" {
			if err := h.store.SetSyncCursor(ctx, h.source.ID, nc, resp.GetFullSyncComplete()); err != nil {
				return fmt.Errorf("pluginhost: persist cursor: %w", err)
			}
		}
	}
	return nil
}

// ActionInvoke is a governed Action invocation over the port (ADR-0047). The
// LAUNCHING Principal (not the plugin's channel identity) and ONLY the
// use-checked, platform-authorized CredentialRef NAMES cross the wire — the
// read-direction mirror of §7's provisioned-cred confinement.
type ActionInvoke struct {
	Principal string // the launching Principal (audit/authz identity, §1.6)
	Action    string
	Args      []byte
	DryRun    bool
	// Credentials are the use-checked, authorized CredentialRefs with their resolved
	// Secret COORDINATES (ADR-0052) — names only, plus coordinates the host attaches to
	// the Envelope on the local path (MF-C). The plugin resolves material itself.
	Credentials          []Credential
	ExpectOutputContract string // core-pinned output-contract id; "" skips the reconcile
}

// ActionEntity is a GOVERNED, UNPROJECTED provision→configure observation —
// kind + identity keys that passed the tier+grant gate. The orchestration
// projects it once, with RUN provenance (per-verb write path, ADR-0047 §2).
type ActionEntity struct {
	Kind         string
	IdentityKeys map[string]string
	// Labels the Action projects (ADR-0058 §6 estate overlay). Carried through to
	// the single Run-provenance projection; unlike a Syncer's labels these are not
	// ownership-gated — a Run write bypasses the label-owner trigger (§4.3), and
	// they are the operator's build-declared overlay, not plugin-owned keys.
	Labels map[string]string
	// Relations the Action projects (a build's placed-in edge, ADR-0059) — the full
	// build observation, not just identity: a build creates infra IN a subnet and
	// projects the topology edge. Targeted BY IDENTITY and gated exactly as a Syncer's
	// (the target scheme must be granted; an unresolved target drops the edge, never
	// vivifies). Facets are NOT here by design — those arrive from the Syncer's poll.
	Relations []GovernedRelation
}

// GovernedRelation is a build's write-back edge to a target named by identity — the
// governed twin of a Syncer's ObservedRelation (ADR-0047 §1 / ADR-0059).
type GovernedRelation struct {
	Type     string
	ToScheme string
	ToValue  string
}

// RawInvokeResult is the governed result of an Action invocation with NOTHING
// written to the graph — the caller performs the single projection.
type RawInvokeResult struct {
	OK               bool
	Outputs          []byte
	Entities         []ActionEntity
	ProvisionedCreds []string
	Rejections       []Rejection
}

// InvokeRaw calls the plugin's Invoke and returns a GOVERNED result WITHOUT
// touching the graph. It applies the host's grant governance (identity-scheme
// gate, ADR-0047 §1/finding #4) to every returned entity, reconciles the
// plugin-asserted output contract against the core-pinned id (§1.5 — drift is
// blocking), namespace-confines provisioned creds (§7), and surfaces rejections.
// "Raw" means unprojected, NEVER ungated.
func (h *Host) InvokeRaw(ctx context.Context, req ActionInvoke) (RawInvokeResult, error) {
	var out RawInvokeResult
	creds := make([]*pluginv1.CredentialRef, 0, len(req.Credentials))
	for _, c := range req.Credentials {
		creds = append(creds, h.wireCred(c))
	}
	stream, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{
		Envelope: &pluginv1.Envelope{
			Principal: &pluginv1.Principal{Id: req.Principal, Kind: "user"},
			Creds:     creds,
		},
		Args:   &pluginv1.Payload{Bytes: req.Args},
		Action: req.Action,
		DryRun: req.DryRun,
	})
	if err != nil {
		return out, fmt.Errorf("pluginhost: invoke %q: %w", req.Action, err)
	}
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, fmt.Errorf("pluginhost: invoke recv: %w", err)
		}
		if ev := resp.GetEvent(); ev != nil && ev.GetTerminal() {
			out.OK = ev.GetOk()
		}
		res := resp.GetResult()
		if res == nil {
			continue // diagnostic message; the result rides the terminal one
		}
		out.Outputs = res.GetOutputs().GetBytes()
		// §1.5: the plugin's asserted output contract must match the core-pinned id.
		if got := res.GetOutputContract().GetSchemaId(); req.ExpectOutputContract != "" && got != "" && got != req.ExpectOutputContract {
			return out, fmt.Errorf("pluginhost: action %q output-contract drift: plugin asserted %q, core pins %q", req.Action, got, req.ExpectOutputContract)
		}
		for _, e := range res.GetEntities() {
			ids := map[string]string{}
			for scheme, val := range e.GetIdentityKeys() {
				if ok, reason := h.grant.allowsIdentity(scheme); !ok {
					r := Rejection{Kind: "identity-scheme", Detail: scheme, Reason: "invoke: " + reason}
					h.reject(r.Kind, r.Detail, r.Reason)
					out.Rejections = append(out.Rejections, r)
					continue
				}
				ids[scheme] = val
			}
			if len(ids) == 0 {
				r := Rejection{Kind: "entity", Detail: e.GetKind(), Reason: "invoke: no granted identity key"}
				h.reject(r.Kind, r.Detail, r.Reason)
				out.Rejections = append(out.Rejections, r)
				continue
			}
			// Relations: a build's write-back edges (ADR-0059), each targeting BY
			// IDENTITY. The target scheme is tier+grant gated exactly as an emitted
			// identity key (ADR-0047 §1); a rejected target drops the edge, never
			// vivifies. Projection (resolve target + upsert) is the single Run-provenance
			// write in RecordActionResult, mirroring the Syncer's two-phase resolve.
			var rels []GovernedRelation
			for _, rel := range e.GetRelations() {
				if ok, reason := h.grant.allowsIdentity(rel.GetToScheme()); !ok {
					r := Rejection{Kind: "relation-target", Detail: rel.GetToScheme(), Reason: "invoke: " + reason}
					h.reject(r.Kind, r.Detail, r.Reason)
					out.Rejections = append(out.Rejections, r)
					continue
				}
				rels = append(rels, GovernedRelation{Type: rel.GetType(), ToScheme: rel.GetToScheme(), ToValue: rel.GetToValue()})
			}
			out.Entities = append(out.Entities, ActionEntity{Kind: e.GetKind(), IdentityKeys: ids, Labels: e.GetLabels(), Relations: rels})
		}
		for _, c := range res.GetProvisionedCreds() {
			if !h.ownsCred(c.GetName()) {
				r := Rejection{Kind: "provisioned-cred", Detail: c.GetName(), Reason: "outside the plugin's credential namespace (ADR-0047 §7)"}
				h.reject(r.Kind, r.Detail, r.Reason)
				out.Rejections = append(out.Rejections, r)
				continue
			}
			out.ProvisionedCreds = append(out.ProvisionedCreds, c.GetName())
		}
	}
	return out, nil
}

// Invoke is the direct/standalone Action path: InvokeRaw + project the governed
// entities with RUN provenance (WriterRun, ADR-0047 §2). The orchestration uses
// InvokeRaw + RecordActionResult instead, so projection stays a single path.
// Register must have run (for the Source-stamped provenance).
func (h *Host) Invoke(ctx context.Context, runID, action string, args []byte, dryRun bool) (InvokeOutcome, error) {
	var out InvokeOutcome
	if h.source.ID == "" {
		return out, errors.New("pluginhost: Invoke before Register")
	}
	raw, err := h.InvokeRaw(ctx, ActionInvoke{Principal: h.grant.PluginIdentity, Action: action, Args: args, DryRun: dryRun})
	if err != nil {
		return out, err
	}
	out.OK = raw.OK
	out.Outputs = raw.Outputs
	out.ProvisionedCreds = raw.ProvisionedCreds
	prov := types.Provenance{WriterKind: types.WriterRun, WriterRef: runID, SourceID: h.source.ID, At: time.Now().UTC()}
	projector := h.store.RunProjector()
	for _, e := range raw.Entities {
		gids, err := projector.UpsertEntities(ctx, prov, []graph.EntityUpsert{{Kind: e.Kind, IdentityKeys: e.IdentityKeys, Labels: e.Labels}})
		if err != nil {
			return out, fmt.Errorf("pluginhost: invoke project: %w", err)
		}
		out.ProvisionedEntity = append(out.ProvisionedEntity, gids...)
		// The build's topology edges (ADR-0059): resolve BY IDENTITY, upsert
		// Run-provenance, drop an unresolved target (no vivify) — same as the orchestrated
		// RecordActionResult path, so projection stays one shape.
		for _, rel := range e.Relations {
			toID, found, err := h.store.EntityIDByIdentity(ctx, rel.ToScheme, rel.ToValue)
			if err != nil {
				return out, fmt.Errorf("pluginhost: invoke resolve relation: %w", err)
			}
			if !found {
				// Visible drop (§1.8), mirroring the Syncer's resolve rejection — never a
				// silent vanish.
				h.reject("relation", rel.Type, "target "+rel.ToScheme+"="+rel.ToValue+" not found; edge dropped (no vivify)")
				continue
			}
			if err := projector.UpsertRelation(ctx, prov, rel.Type, gids[0], toID); err != nil {
				return out, fmt.Errorf("pluginhost: invoke project relation: %w", err)
			}
		}
	}
	return out, nil
}

// ApplyTarget is one core-resolved actuation target, passed LEGIBLY to the plugin
// (ADR-0047 §1.1): the target set carries blast-radius/authz weight and is the
// correlation key, so it is NEVER baked into the opaque desired payload. Name is
// the confused-deputy gate key; Address is the typed reachability coordinate
// (ADR-0084 — the plugin renders its own connection var from it, the spine never
// authors one); Vars are genuinely tool-authored vars only; IdentityKeys
// re-correlate write-back to the target Entity.
type ApplyTarget struct {
	Name         string
	Address      string
	IdentityKeys map[string]string
	Vars         map[string]string
}

// ApplyInvoke is a governed Actuator Apply request. Params is the opaque tool
// config (`desired`); Targets is the legible core-resolved set; CredentialRefs
// are authorized NAMES only (§2.5, the credential-oracle closure). PlanDigest +
// PinnedPlan carry a Gate-approved pinned plan (ADR-0047 §8): the caller has
// already fetched-and-re-hashed the bytes from the core store, so ApplyRaw sends
// them for the plugin to apply EXACTLY — never a plan the plugin re-resolves.
type ApplyInvoke struct {
	Principal      string
	Params         []byte
	Targets        []ApplyTarget
	DryRun         bool
	CredentialRefs []string
	PlanDigest     string
	PinnedPlan     []byte
	// FacetWriteScope is the per-Run facet FLOOR (ADR-0054): the effective write-back
	// allowlist is the plugin grant ∩ this scope. Empty admits no facet write-back.
	FacetWriteScope []string
	// ResolvedCapabilities are the core-resolved capability handles (ADR-0105), keyed by capability
	// class (e.g. "statestore"). Injected LEGIBLY onto the Apply (never the opaque `desired`) when
	// the Actuator `requires` a capability; the plugin consumes the handle (e.g. tofu -backend-config).
	ResolvedCapabilities map[string]CapabilityHandle
}

// CapabilityHandle is a core-resolved capability's provider-agnostic handle (ADR-0105): Kind is the
// provider-neutral variant (for statestore, the tool backend type — s3/http/gcs), Config its string
// settings, CredentialRef a §2.5 CredentialRef NAME (resolved at the pod, never material).
type CapabilityHandle struct {
	Kind          string
	Config        map[string]string
	CredentialRef string
	// Output is the contract-validated resolve output bytes verbatim (ADR-0112 D2) — carried so a
	// non-statestore-shaped handle (ipam's {cidr,…}) reaches its consumer; the consumer decodes it
	// against its own capabilities/<class>.output Contract.
	Output []byte
}

// wireCapabilities translates the core-resolved capability handles onto the wire — shared by the
// Apply and Plan paths so both carry the SAME handle (ADR-0105: a pinned plan must be computed
// against the same state backend it is applied to). Nil/empty ⇒ nil (no injection).
func wireCapabilities(in map[string]CapabilityHandle) map[string]*pluginv1.CapabilityHandle {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]*pluginv1.CapabilityHandle, len(in))
	for class, ch := range in {
		out[class] = &pluginv1.CapabilityHandle{Kind: ch.Kind, Config: ch.Config, CredentialRef: ch.CredentialRef, Output: ch.Output}
	}
	return out
}

// PlanInvoke is a governed Actuator Plan request (the unary, pinnable producer).
type PlanInvoke struct {
	Principal      string
	Params         []byte
	CredentialRefs []string
	// ResolvedCapabilities are the core-resolved handles (ADR-0105) — the SAME the Apply gets, so
	// the pinned plan is computed against the same state backend it will be applied to.
	ResolvedCapabilities map[string]CapabilityHandle
}

// PlanOutcome is the governed result of a Plan. Digest is the content-address of
// the saved plan the CORE computed + stored (the pin a Gate binds); "" when the
// plan is empty. Diff is the plugin-redacted descent diff; the core never
// interprets it (§1.8/§2.5).
type PlanOutcome struct {
	Digest  string
	Summary string
	Empty   bool
	Diff    []byte
}

// Plan calls the plugin's unary Plan verb and content-addresses the saved plan:
// the CORE computes the sha256 (a plugin-asserted plan.sha256 is advisory, §1.5),
// encrypts + stores it write-once, and returns the digest. This is the canonical
// producer of the pin a Gate approves; the streaming dry-run Apply cannot produce
// one (ApplyResponse has no saved-plan field — structurally non-pinnable).
func (h *Host) Plan(ctx context.Context, req PlanInvoke) (PlanOutcome, error) {
	var out PlanOutcome
	creds := make([]*pluginv1.CredentialRef, 0, len(req.CredentialRefs))
	for _, n := range req.CredentialRefs {
		creds = append(creds, &pluginv1.CredentialRef{Name: n})
	}
	resp, err := h.client.Plan(ctx, &pluginv1.PlanRequest{
		Envelope: &pluginv1.Envelope{
			Principal: &pluginv1.Principal{Id: req.Principal, Kind: "user"},
			Creds:     creds,
		},
		Desired:              &pluginv1.Payload{Bytes: req.Params},
		ResolvedCapabilities: wireCapabilities(req.ResolvedCapabilities),
	})
	if err != nil {
		return out, fmt.Errorf("pluginhost: plan: %w", err)
	}
	out.Summary = resp.GetSummary()
	out.Empty = resp.GetEmpty()
	out.Diff = resp.GetDiff().GetBytes()
	if saved := resp.GetSavedPlan(); len(saved) > 0 {
		if h.plans == nil {
			return out, errors.New("pluginhost: Plan produced a saved plan but no plan store is attached (UsePlanStore)")
		}
		digest, err := h.plans.Put(ctx, saved) // CORE computes the sha256, encrypts, write-once
		if err != nil {
			return out, fmt.Errorf("pluginhost: store plan: %w", err)
		}
		out.Digest = digest
	}
	return out, nil
}

// VerifyPinnedPlan fetches the plan at the Gate-approved digest from the core
// store and RE-HASHES it (verify-don't-trust, ADR-0047 §8) — the caller passes the
// returned bytes to ApplyRaw as the pinned plan. A missing/tampered plan is
// terminal (fail closed — never a silent unpinned apply).
func (h *Host) VerifyPinnedPlan(ctx context.Context, digest string) ([]byte, error) {
	if h.plans == nil {
		return nil, errors.New("pluginhost: plan-pinned Apply but no plan store is attached")
	}
	return h.plans.GetVerified(ctx, digest)
}

// ApplyEntity is a GOVERNED, UNPROJECTED write-back observation from an Apply
// (tofu stratt_entities, ansible gathered facts). The caller projects it ONCE,
// with RUN provenance (WriterRun) — never Syncer, never the plugin (per-verb
// write path is a t=0 invariant, ADR-0047 §2). "Raw" is unprojected, never
// ungated: identity schemes passed the tier+grant gate.
type ApplyEntity struct {
	Kind         string
	IdentityKeys map[string]string
	Labels       map[string]string
	Facets       map[string][]byte
}

// DerivedSchema is a plugin-emitted rung-2 (tofu-plan-derived) output schema
// DOCUMENT. The caller recomputes + pins its hash (no plugin-asserted sha256,
// §1.5); a new rev with different bytes is blocking drift. schema_id is confined
// to the plugin's own Source namespace (ADR-0047 §4) — the host rejects any id
// outside it, so a plugin can never overwrite another owner's contract.
type DerivedSchema struct {
	SchemaID string
	Rev      string
	Schema   []byte
	// Rung carries the wire DerivedContract.rung (ADR-0053 MF-1): the caller MUST
	// branch on it — RUNG_DECLARED (3) pins BLOCKING at the given rev (mcp rung-3),
	// RUNG_TOOL_DERIVED (2) auto-versions (tofu rung-2), UNSPECIFIED/unknown is a HARD
	// REJECT (never a silent auto-version). Dropping it would silently absorb schema
	// drift — the §1.5 violation. -1 is never a valid rung.
	Rung int32
}

// RawApplyResult is the governed outcome of an Apply with NOTHING written to the
// graph — the orchestration performs the single batched projection AFTER the
// stream is fully consumed (guardian fix #2: never interleave a graph write from
// the Recv loop; Execute is a retryable activity). Succeeded is COMPUTED
// core-side from the per-target statuses (guardian fix #3), never the plugin's
// self-asserted terminal ok — a plugin that returns ok=true alongside a FAILED
// target still yields a non-OK Run (§1.8).
type RawApplyResult struct {
	Succeeded  bool
	PerTarget  map[string]string // resolved target name -> status; sticky-fail folded
	WriteBack  []ApplyEntity
	Drift      map[string][]json.RawMessage
	Derived    []DerivedSchema
	Checkpoint string // graceful-abort resume token (invariant #7); "" == ran to completion
	Rejections []Rejection
}

// applyStatus renders a wire ItemResult.Status as the core-legible per-target
// string (dispatch.Result.PerTarget convention).
func applyStatus(s pluginv1.ItemResult_Status) string {
	switch s {
	case pluginv1.ItemResult_STATUS_OK:
		return "ok"
	case pluginv1.ItemResult_STATUS_CHANGED:
		return "changed"
	case pluginv1.ItemResult_STATUS_FAILED:
		return "failed"
	case pluginv1.ItemResult_STATUS_UNREACHABLE:
		return "unreachable"
	default:
		return "unspecified"
	}
}

// ApplyRaw calls the plugin's Apply and returns a GOVERNED result WITHOUT touching
// the graph. It streams ApplyResponses, folding per-target status core-side,
// gating write-back identity schemes and derived-contract namespaces on the grant,
// and rejecting any per-target status keyed to a target OUTSIDE the resolved set
// (confused-deputy, guardian fix #1). The terminal `ok` is deliberately ignored
// (guardian fix #3). Nothing is projected here — the caller writes once (fix #2).
func (h *Host) ApplyRaw(ctx context.Context, req ApplyInvoke) (RawApplyResult, error) {
	out := RawApplyResult{PerTarget: map[string]string{}}
	// The resolved target-name set is the confused-deputy gate.
	resolved := make(map[string]bool, len(req.Targets))
	targets := make([]*pluginv1.ApplyTarget, 0, len(req.Targets))
	for _, t := range req.Targets {
		resolved[t.Name] = true
		targets = append(targets, &pluginv1.ApplyTarget{Name: t.Name, Address: t.Address, IdentityKeys: t.IdentityKeys, Vars: t.Vars})
	}
	creds := make([]*pluginv1.CredentialRef, 0, len(req.CredentialRefs))
	for _, n := range req.CredentialRefs {
		creds = append(creds, &pluginv1.CredentialRef{Name: n})
	}
	applyReq := &pluginv1.ApplyRequest{
		Envelope: &pluginv1.Envelope{
			Principal: &pluginv1.Principal{Id: req.Principal, Kind: "user"},
			Creds:     creds,
		},
		Desired: &pluginv1.Payload{Bytes: req.Params},
		DryRun:  req.DryRun,
		Targets: targets,
	}
	// A Gate-approved pinned plan: the core-verified bytes + the digest the plugin
	// applies EXACTLY (never a plan it re-resolves, ADR-0047 §8).
	if len(req.PinnedPlan) > 0 {
		applyReq.PlanRef = &pluginv1.ArtifactRef{Sha256: req.PlanDigest}
		applyReq.PinnedPlan = req.PinnedPlan
	}
	// Core-resolved capability handles (ADR-0105) ride the LEGIBLE channel, never `desired`.
	applyReq.ResolvedCapabilities = wireCapabilities(req.ResolvedCapabilities)
	stream, err := h.client.Apply(ctx, applyReq)
	if err != nil {
		return out, fmt.Errorf("pluginhost: apply: %w", err)
	}
	// One governor, transport-agnostic (ADR-0051 MF1): the gRPC stream and the
	// EE-Job stdout adapter both feed the SAME govern loop; the dispatcher and Site
	// agents relay these shapes but fold/gate nothing. The facet floor (ADR-0054) is
	// applied here, at the one governor — never re-implemented per transport.
	return h.govern(ctx, stream, resolved, scopeSet(req.FacetWriteScope))
}

// applyStream is the transport-agnostic source of ApplyResponses the governor
// consumes (ADR-0051): the gRPC client stream satisfies it, and so does the EE-Job
// stdout adapter (jsonLineStream). io.EOF ends the stream.
type applyStream interface {
	Recv() (*pluginv1.ApplyResponse, error)
}

// GovernStream is the entry the EE-Job (subprocess) transport uses (ADR-0051 MF1):
// it governs a stream of typed ApplyResponses the Job emitted on stdout, against the
// CORE-HELD resolved target set (MF4 — the confused-deputy gate keys on these names,
// never the pod's self-reported inventory). Identical governance to the gRPC path.
func (h *Host) GovernStream(ctx context.Context, stream applyStream, targets []ApplyTarget, writeScope []string) (RawApplyResult, error) {
	resolved := make(map[string]bool, len(targets))
	for _, t := range targets {
		resolved[t.Name] = true
	}
	return h.govern(ctx, stream, resolved, scopeSet(writeScope))
}

// scopeSet builds the per-Run facet write-scope lookup (ADR-0054). The set is the
// FLOOR intersected with the grant ceiling at govern; TIGHT default — a nil/empty
// scope admits NO facet write-back (least authority).
func scopeSet(ns []string) map[string]bool {
	s := make(map[string]bool, len(ns))
	for _, n := range ns {
		s[n] = true
	}
	return s
}

// govern is the SOLE hub-side Apply governor (ADR-0051): confused-deputy target gate,
// identity/facet/label write-back gates, drift/derived-contract capture, and the
// core-side Succeeded fold (a stream that never sent a terminal folds to not-OK —
// the §1.8 silent-death floor). `resolved` is the core-held resolved target-name set;
// `writeScope` is the per-Run facet FLOOR — the effective facet allowlist is
// grant ∩ writeScope (ADR-0054, pure AND — scoped-but-ungranted just drops).
func (h *Host) govern(ctx context.Context, stream applyStream, resolved, writeScope map[string]bool) (RawApplyResult, error) {
	out := RawApplyResult{PerTarget: map[string]string{}}
	var failed, sawTerminal bool
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, fmt.Errorf("pluginhost: apply recv: %w", err)
		}
		if ev := resp.GetEvent(); ev != nil {
			if cp := ev.GetCheckpoint(); cp != "" {
				out.Checkpoint = cp
			}
			if ev.GetTerminal() {
				sawTerminal = true // ev.GetOk() intentionally ignored — fold below
			}
		}
		// Per-target status: confused-deputy gated, sticky-fail folded.
		if r := resp.GetResult(); r != nil {
			key := r.GetItemKey()
			switch {
			case key != "" && !resolved[key]:
				rej := Rejection{Kind: "item-result", Detail: key, Reason: "apply: per-target status for a target outside the resolved set (confused deputy)"}
				h.reject(rej.Kind, rej.Detail, rej.Reason)
				out.Rejections = append(out.Rejections, rej)
			default:
				st := applyStatus(r.GetStatus())
				if st == "failed" || st == "unreachable" {
					failed = true
				}
				if key != "" {
					if prev := out.PerTarget[key]; prev != "failed" && prev != "unreachable" {
						out.PerTarget[key] = st // sticky: a failed target is never downgraded
					}
				}
			}
		}
		// Write-back: identity-scheme tier+grant gate, UNPROJECTED (mirrors InvokeRaw).
		for _, e := range resp.GetWriteBack() {
			ids := map[string]string{}
			for scheme, val := range e.GetIdentityKeys() {
				if ok, reason := h.grant.allowsIdentity(scheme); !ok {
					rej := Rejection{Kind: "identity-scheme", Detail: scheme, Reason: "apply: " + reason}
					h.reject(rej.Kind, rej.Detail, rej.Reason)
					out.Rejections = append(out.Rejections, rej)
					continue
				}
				ids[scheme] = val
			}
			if len(ids) == 0 {
				rej := Rejection{Kind: "entity", Detail: e.GetKind(), Reason: "apply: no granted identity key"}
				h.reject(rej.Kind, rej.Detail, rej.Reason)
				out.Rejections = append(out.Rejections, rej)
				continue
			}
			labels := map[string]string{}
			for k, v := range e.GetLabels() {
				if !h.grant.allowsLabel(k) {
					rej := Rejection{Kind: "label", Detail: k, Reason: "apply: label key not in operator grant"}
					h.reject(rej.Kind, rej.Detail, rej.Reason)
					out.Rejections = append(out.Rejections, rej)
					continue
				}
				labels[k] = v
			}
			facets := map[string][]byte{}
			for ns, v := range e.GetFacets() {
				// Effective allowlist = grant ceiling ∩ Step write-scope (ADR-0054):
				// pure AND — a facet outside EITHER bound drops, never a fallback (§2.4).
				if !h.grant.allowsFacet(ns) {
					rej := Rejection{Kind: "facet", Detail: ns, Reason: "apply: facet namespace not in operator grant"}
					h.reject(rej.Kind, rej.Detail, rej.Reason)
					out.Rejections = append(out.Rejections, rej)
					continue
				}
				if !writeScope[ns] {
					rej := Rejection{Kind: "facet", Detail: ns, Reason: "apply: facet namespace not in the Step's facet write-scope (least authority, ADR-0054)"}
					h.reject(rej.Kind, rej.Detail, rej.Reason)
					out.Rejections = append(out.Rejections, rej)
					continue
				}
				facets[ns] = v
			}
			out.WriteBack = append(out.WriteBack, ApplyEntity{Kind: e.GetKind(), IdentityKeys: ids, Labels: labels, Facets: facets})
		}
		// Drift: opaque, already-redacted, accumulated per item_key (ADR-0019).
		if d := resp.GetDrift(); d != nil {
			if out.Drift == nil {
				out.Drift = map[string][]json.RawMessage{}
			}
			out.Drift[d.GetItemKey()] = append(out.Drift[d.GetItemKey()], json.RawMessage(d.GetDetail().GetBytes()))
		}
		// Derived contract: namespace-confined to the plugin's own Source scope.
		if dc := resp.GetDerivedContract(); dc != nil {
			id := dc.GetSchemaId()
			if !strings.HasPrefix(id, h.grant.Source.Name+"/") {
				rej := Rejection{Kind: "derived-contract", Detail: id, Reason: "apply: schema_id outside the plugin's Source namespace (ADR-0047 §4)"}
				h.reject(rej.Kind, rej.Detail, rej.Reason)
				out.Rejections = append(out.Rejections, rej)
			} else {
				// Carry the rung (MF-1): the caller branches on it (rung-3 blocking pin /
				// rung-2 auto-version / unknown reject) — never a silent default.
				out.Derived = append(out.Derived, DerivedSchema{
					SchemaID: id, Rev: dc.GetRev(), Schema: dc.GetSchema(), Rung: int32(dc.GetRung()),
				})
			}
		}
	}
	// Core-side fold (guardian fix #3): the plugin's terminal ok is NOT trusted.
	// A stream that never terminated is also a failure (partial/torn stream).
	out.Succeeded = sawTerminal && !failed
	return out, nil
}

// jsonLineStream adapts a sequence of proto-JSON ApplyResponse lines (an EE-Job's
// stdout — the subprocess transport, ADR-0051) into an applyStream the governor
// consumes. next() returns the next line, io.EOF at end. The dispatcher pre-routes
// non-ApplyResponse lines (ansible-runner banners, tracebacks) to the §1.8
// diagnostic ring (MF5), so a line reaching here should decode; a stray undecodable
// line is a hard error, never silently dropped.
type jsonLineStream struct {
	next func() ([]byte, error)
}

// NewJobStream builds an applyStream over an EE-Job's typed stdout line source — the
// EE-Job (subprocess) transport's feed into the one governor (ADR-0051 MF1).
func NewJobStream(next func() ([]byte, error)) applyStream { return &jsonLineStream{next: next} }

func (s *jsonLineStream) Recv() (*pluginv1.ApplyResponse, error) {
	line, err := s.next()
	if err != nil {
		return nil, err // io.EOF or a read error
	}
	resp := &pluginv1.ApplyResponse{}
	if uerr := protojson.Unmarshal(line, resp); uerr != nil {
		return nil, fmt.Errorf("pluginhost: undecodable ApplyResponse line: %w", uerr)
	}
	return resp, nil
}

// chanStream adapts a channel of already-decoded ApplyResponses into an applyStream
// (ADR-0051 MF1): the dispatcher decodes the EE-Job's typed stdout and pushes each
// response here, the governor pulls them off. A closed channel is io.EOF; a canceled
// ctx short-circuits so a stuck Job never wedges the governor.
type chanStream struct {
	ctx context.Context
	ch  <-chan *pluginv1.ApplyResponse
}

// NewChanStream builds an applyStream over a channel of decoded ApplyResponses — the
// EE-Job (subprocess) transport's feed into the one governor (GovernStream).
func NewChanStream(ctx context.Context, ch <-chan *pluginv1.ApplyResponse) applyStream {
	return &chanStream{ctx: ctx, ch: ch}
}

func (s *chanStream) Recv() (*pluginv1.ApplyResponse, error) {
	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case resp, ok := <-s.ch:
		if !ok {
			return nil, io.EOF
		}
		return resp, nil
	}
}

// ownsCred enforces provisioned-CredentialRef namespace confinement (ADR-0047 §7):
// a plugin may only name creds under its own Source scope, so it cannot shadow
// another principal's CredentialRef. (Registration into the secrets registry is
// the follow-up; this gates the name.)
func (h *Host) ownsCred(name string) bool {
	return strings.HasPrefix(name, "cred/"+h.grant.Source.Name+"/")
}

// EventPublisher is the seam onto the emitter-event stream (events.Bus satisfies
// it). The Emitter host publishes the plugin's core-legible `match` projection
// as a types.EmitterEvent under the GRANT's emitter name.
type EventPublisher interface {
	PublishEmitterEvent(ctx context.Context, ev types.EmitterEvent) error
}

// SubscribeLoop drives an Emitter plugin (ADR-0046/0047, the Emitter verb): it
// BINDS the emitter name to the authenticated channel identity (the plugin's
// manifest plugin_id must equal the granted identity — the anti-spoof gate the
// guardian required), holds a long-lived Subscribe stream (reconnecting with
// backoff), and publishes each event's core-legible `match` as a
// types.EmitterEvent under the GRANT's emitter name — NEVER the plugin's
// subject/type. CEL matches this, never the opaque payload (ADR-0047 §3). No
// graph write, no Source ownership (§1.2 N/A — events, not entities). Emission
// is gated by the operator grant of the emitter name; a malformed event is
// dropped with a visible rejection (§1.8), never silently.
func (h *Host) SubscribeLoop(ctx context.Context, pub EventPublisher) error {
	// Bind the emitter name to the channel identity — the Register-equivalent for
	// the Emitter verb (no Source, but the same identity binding).
	man, err := h.client.GetManifest(ctx, &pluginv1.GetManifestRequest{})
	if err != nil {
		return fmt.Errorf("pluginhost: emitter manifest: %w", err)
	}
	if id := man.GetManifest().GetPluginId(); id != h.grant.PluginIdentity {
		return fmt.Errorf("pluginhost: emitter manifest identity %q != granted identity %q (anti-spoof)", id, h.grant.PluginIdentity)
	}
	emitter := h.grant.emitterName()
	h.log.Info("emitter subscribed", "emitter", emitter)

	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := h.subscribeOnce(ctx, pub, emitter)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		h.log.Warn("emitter stream ended; reconnecting", "emitter", emitter, "error", err, "backoff", backoff.String())
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (h *Host) subscribeOnce(ctx context.Context, pub EventPublisher, emitter string) error {
	stream, err := h.client.Subscribe(ctx, &pluginv1.SubscribeRequest{})
	if err != nil {
		return err
	}
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil // clean end; the loop reconnects
		}
		if err != nil {
			return err
		}
		ev := resp.GetEvent()
		m := ev.GetMatch()
		if m == nil {
			// A malformed event with no legible routing projection is dropped
			// VISIBLY (§1.8) — the plugin's subject/type never become the route.
			h.reject("emitter", emitter, "event has no core-legible match projection; dropped")
			continue
		}
		pubEvent := types.EmitterEvent{
			Emitter:    emitter, // grant-bound; the plugin cannot influence the trigger route
			ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Payload:    m.AsMap(), // the same map[string]any shape CEL already consumes
		}
		if err := pub.PublishEmitterEvent(ctx, pubEvent); err != nil {
			h.reject("emitter", emitter, "publish failed: "+err.Error())
		}
	}
}

// toUpsert gates one ObservedEntity into a graph.EntityUpsert: it drops every
// identity scheme / label key / facet namespace outside the grant (recording a
// Rejection), and requires at least one granted identity key. Facet VALUES stay
// blobs — they are validated against the pinned schema at the write path.
func (h *Host) toUpsert(e *pluginv1.ObservedEntity) (graph.EntityUpsert, bool) {
	ids := map[string]string{}
	for scheme, val := range e.GetIdentityKeys() {
		if ok, reason := h.grant.allowsIdentity(scheme); !ok {
			h.reject("identity-scheme", scheme, reason)
			continue
		}
		ids[scheme] = val
	}
	if len(ids) == 0 {
		h.reject("entity", e.GetKind(), "no granted identity key; cannot project without identity")
		return graph.EntityUpsert{}, false
	}

	labels := map[string]string{}
	for k, v := range e.GetLabels() {
		if !h.grant.allowsLabel(k) {
			h.reject("label", k, "label key not in operator grant")
			continue
		}
		labels[k] = v
	}

	facets := map[string]json.RawMessage{}
	for ns, blob := range e.GetFacets() {
		if !h.grant.allowsFacet(ns) {
			h.reject("facet", ns, "facet namespace not in operator grant")
			continue
		}
		facets[ns] = json.RawMessage(blob)
	}

	return graph.EntityUpsert{Kind: e.GetKind(), IdentityKeys: ids, Labels: labels, Facets: facets}, true
}

// WireCredForTest exposes wireCred to the package's external-facing tests (the
// enrichment boundary is security-critical, ADR-0052 MF-C, and must be asserted).
func (h *Host) WireCredForTest(c Credential) *pluginv1.CredentialRef { return h.wireCred(c) }
