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
	Kind   string // "identity-scheme" | "label" | "facet" | "entity"
	Detail string // the offending scheme/key/namespace
	Reason string
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
			_ = ids
			for _, s := range h.grant.TombstoneSchemes {
				if v, ok := up.IdentityKeys[s]; ok {
					seen[s] = append(seen[s], v)
				}
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
