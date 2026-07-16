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

	"github.com/dstout-devops/stratt/core/internal/graph"
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
	return &Host{store: store, client: client, grant: grant,
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
	if m.GetClass() != pluginv1.PluginClass_PLUGIN_CLASS_SYNCER {
		return fmt.Errorf("pluginhost: plugin %q is not a Syncer (class %v)", h.grant.PluginIdentity, m.GetClass())
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
		if err := h.store.RegisterFacetOwner(ctx, types.FacetOwner{Namespace: ns, OwnerKind: "syncer", OwnerRef: ref}); err != nil {
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
	stream, err := h.client.Observe(ctx, &pluginv1.ObserveRequest{})
	if err != nil {
		return fmt.Errorf("pluginhost: observe: %w", err)
	}
	prov := h.provenance()
	projector := h.store.NormalizerProjector()
	seen := map[string][]string{} // tombstone scheme -> seen values this full sync

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

		if resp.GetFullSyncComplete() {
			for _, s := range h.grant.TombstoneSchemes {
				if _, err := projector.TombstoneAbsent(ctx, prov, s, seen[s]); err != nil {
					return fmt.Errorf("pluginhost: tombstone absent %q: %w", s, err)
				}
			}
		}
	}
	return nil
}

// Invoke runs one Action over the port (ADR-0047): it calls the plugin's Invoke,
// streams TaskEvents (audit/descent), and on the terminal message captures the
// InvokeResult — projecting its entities with RUN provenance (WriterRun; the host
// picks the write path per verb, never the plugin — ADR-0047 §2), gating identity
// schemes exactly as on Observe, and returning the typed outputs. runID stamps
// provenance; Register must have run.
func (h *Host) Invoke(ctx context.Context, runID, action string, args []byte, dryRun bool) (InvokeOutcome, error) {
	var out InvokeOutcome
	if h.source.ID == "" {
		return out, errors.New("pluginhost: Invoke before Register")
	}
	stream, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{
		Envelope: &pluginv1.Envelope{Principal: &pluginv1.Principal{Id: h.grant.PluginIdentity, Kind: "service"}},
		Args:     &pluginv1.Payload{Bytes: args},
		Action:   action,
		DryRun:   dryRun,
	})
	if err != nil {
		return out, fmt.Errorf("pluginhost: invoke %q: %w", action, err)
	}
	// RUN provenance — the imperative Action write path, distinct from the Syncer
	// Observe path (per-verb projector selection, ADR-0047 §2).
	prov := types.Provenance{WriterKind: types.WriterRun, WriterRef: runID, SourceID: h.source.ID, At: time.Now().UTC()}
	projector := h.store.RunProjector()

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
		if ev := resp.GetEvent(); ev != nil {
			out.Events++
			if ev.GetTerminal() {
				out.OK = ev.GetOk()
			}
		}
		res := resp.GetResult()
		if res == nil {
			continue // diagnostic message; the result rides the terminal one
		}
		out.Outputs = res.GetOutputs().GetBytes()
		out.OutputContract = res.GetOutputContract().GetSchemaId()

		// provision→configure: project each entity by kind + granted identity with
		// RUN provenance. (Facet/label write-back on the Invoke path — with the same
		// ownership rules as Observe — is the recorded follow-up; identity is what
		// lets the next Syncer sweep correlate onto the provisioned entity.)
		for _, e := range res.GetEntities() {
			ids := map[string]string{}
			for scheme, val := range e.GetIdentityKeys() {
				if ok, reason := h.grant.allowsIdentity(scheme); !ok {
					h.reject("identity-scheme", scheme, "invoke: "+reason)
					continue
				}
				ids[scheme] = val
			}
			if len(ids) == 0 {
				h.reject("entity", e.GetKind(), "invoke: no granted identity key")
				continue
			}
			gids, err := projector.UpsertEntities(ctx, prov, []graph.EntityUpsert{{Kind: e.GetKind(), IdentityKeys: ids}})
			if err != nil {
				return out, fmt.Errorf("pluginhost: invoke project: %w", err)
			}
			out.ProvisionedEntity = append(out.ProvisionedEntity, gids...)
		}
		for _, c := range res.GetProvisionedCreds() {
			name := c.GetName()
			if !h.ownsCred(name) {
				h.reject("provisioned-cred", name, "outside the plugin's credential namespace (ADR-0047 §7)")
				continue
			}
			out.ProvisionedCreds = append(out.ProvisionedCreds, name)
		}
	}
	return out, nil
}

// ownsCred enforces provisioned-CredentialRef namespace confinement (ADR-0047 §7):
// a plugin may only name creds under its own Source scope, so it cannot shadow
// another principal's CredentialRef. (Registration into the secrets registry is
// the follow-up; this gates the name.)
func (h *Host) ownsCred(name string) bool {
	return strings.HasPrefix(name, "cred/"+h.grant.Source.Name+"/")
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
