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
	Principal            string // the launching Principal (audit/authz identity, §1.6)
	Action               string
	Args                 []byte
	DryRun               bool
	CredentialRefs       []string // authorized names only (the credential-oracle closure)
	ExpectOutputContract string   // core-pinned output-contract id; "" skips the reconcile
}

// ActionEntity is a GOVERNED, UNPROJECTED provision→configure observation —
// kind + identity keys that passed the tier+grant gate. The orchestration
// projects it once, with RUN provenance (per-verb write path, ADR-0047 §2).
type ActionEntity struct {
	Kind         string
	IdentityKeys map[string]string
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
	creds := make([]*pluginv1.CredentialRef, 0, len(req.CredentialRefs))
	for _, n := range req.CredentialRefs {
		creds = append(creds, &pluginv1.CredentialRef{Name: n})
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
			out.Entities = append(out.Entities, ActionEntity{Kind: e.GetKind(), IdentityKeys: ids})
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
		gids, err := projector.UpsertEntities(ctx, prov, []graph.EntityUpsert{{Kind: e.Kind, IdentityKeys: e.IdentityKeys}})
		if err != nil {
			return out, fmt.Errorf("pluginhost: invoke project: %w", err)
		}
		out.ProvisionedEntity = append(out.ProvisionedEntity, gids...)
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
