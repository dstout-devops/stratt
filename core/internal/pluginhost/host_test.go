package pluginhost_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	neturl "net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// newTestStore mirrors graph's own test harness: a throwaway migrated database
// on the dev-substrate Postgres. Skips when none is reachable.
func newTestStore(t *testing.T) *graph.Store {
	t.Helper()
	url := os.Getenv("STRATT_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://stratt:stratt-dev@localhost:5432/stratt"
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}
	name := fmt.Sprintf("stratt_ph_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close()
	})
	u, _ := neturl.Parse(url)
	u.Path = "/" + name
	store, err := graph.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect+migrate: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

// fakePlugin is a canned Syncer plugin — no govmomi, no external system. It lets
// the host test exercise the full grant → provenance → graph path over a real
// gRPC connection while staying inside the core module (module isolation: the
// core suite never imports a domain plugin).
type fakePlugin struct {
	pluginv1.UnimplementedPluginServiceServer
	pluginID               string
	class                  pluginv1.PluginClass
	contracts              []*pluginv1.ContractDecl
	tombstoneSchemes       []string
	entities               []*pluginv1.ObservedEntity
	invokeEntities         []*pluginv1.ObservedEntity
	invokeOutputs          []byte
	invokeCreds            []string
	invokeOutputContractID string
	// delta-cursor knobs: on an empty-cursor (full) Observe, NextCursor=nextCursor;
	// on a cursored (delta) Observe, emit deltaGone + NextCursor=deltaCursor.
	nextCursor  string
	deltaGone   []*pluginv1.GoneEntity
	deltaCursor string
}

func (f *fakePlugin) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	class := f.class
	if class == pluginv1.PluginClass_PLUGIN_CLASS_UNSPECIFIED {
		class = pluginv1.PluginClass_PLUGIN_CLASS_SYNCER
	}
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:         f.pluginID,
		ProtocolVersion:  "v1",
		Class:            class,
		Verbs:            []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE},
		Contracts:        f.contracts,
		TombstoneSchemes: f.tombstoneSchemes,
	}}, nil
}

func (f *fakePlugin) Observe(req *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	if req.GetCursor() == "" {
		return stream.Send(&pluginv1.ObserveResponse{Entities: f.entities, FullSyncComplete: true, NextCursor: f.nextCursor})
	}
	// Delta window: the host resumed from a persisted cursor.
	return stream.Send(&pluginv1.ObserveResponse{Gone: f.deltaGone, NextCursor: f.deltaCursor})
}

func (f *fakePlugin) Invoke(_ *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	_ = stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{Level: pluginv1.TaskEvent_LEVEL_INFO, Message: "working"}})
	var creds []*pluginv1.CredentialRef
	for _, c := range f.invokeCreds {
		creds = append(creds, &pluginv1.CredentialRef{Name: c})
	}
	ocID := f.invokeOutputContractID
	if ocID == "" {
		ocID = "action.output"
	}
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{Level: pluginv1.TaskEvent_LEVEL_INFO, Message: "done", Terminal: true, Ok: true},
		Result: &pluginv1.InvokeResult{
			Outputs:          &pluginv1.Payload{Bytes: f.invokeOutputs},
			OutputContract:   &pluginv1.ContractRef{SchemaId: ocID},
			Entities:         f.invokeEntities,
			ProvisionedCreds: creds,
		},
	})
}

func serve(t *testing.T, f *fakePlugin) pluginv1.PluginServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(); srv.Stop(); _ = lis.Close() })
	return pluginv1.NewPluginServiceClient(conn)
}

func vcenterGrant(tier pluginhost.Tier, identitySchemes []string) pluginhost.Grant {
	return pluginhost.Grant{
		PluginIdentity:   "vcenter-dev",
		Tier:             tier,
		Source:           types.Source{Kind: "vcenter", Name: "vcenter-dev", Endpoint: "https://vcsim/sdk"},
		FacetNamespaces:  []string{"vm.config", "vm.runtime", "net.guest"},
		LabelKeys:        []string{"vcenter.name"},
		IdentitySchemes:  identitySchemes,
		TombstoneSchemes: []string{"vcenter.uuid"},
	}
}

func ent(uuid string, ids map[string]string, facets map[string][]byte) *pluginv1.ObservedEntity {
	k := map[string]string{"vcenter.uuid": uuid}
	for s, v := range ids {
		k[s] = v
	}
	return &pluginv1.ObservedEntity{
		Kind: "vm", IdentityKeys: k,
		Labels: map[string]string{"vcenter.name": "vm-" + uuid},
		Facets: facets,
	}
}

func vms(t *testing.T, store *graph.Store) []types.Entity {
	t.Helper()
	es, err := store.ResolveSelector(context.Background(), types.ViewSelector{Kinds: []string{"vm"}}, nil, 100)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return es
}

// TestHost_TrustedSync_ProjectsWithProvenanceFromChannel proves the full wire
// path: a granted plugin's Observe lands Entities in the graph with provenance
// stamped from the channel identity (the plugin never touched the DB).
func TestHost_TrustedSync_ProjectsWithProvenanceFromChannel(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	grant := vcenterGrant(pluginhost.TierTrusted, []string{"vcenter.uuid"})
	client := serve(t, &fakePlugin{
		pluginID:         "vcenter-dev",
		contracts:        []*pluginv1.ContractDecl{{SchemaId: "vm.config", Band: "S3"}},
		tombstoneSchemes: []string{"vcenter.uuid"},
		entities: []*pluginv1.ObservedEntity{
			ent("u1", nil, map[string][]byte{"vm.config": []byte(`{"cpus":2}`)}),
			ent("u2", nil, map[string][]byte{"vm.config": []byte(`{"cpus":4}`)}),
		},
	})
	h := pluginhost.New(store, client, grant, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := h.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := h.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	got := vms(t, store)
	if len(got) != 2 {
		t.Fatalf("expected 2 vms projected, got %d", len(got))
	}
	// Provenance came from the channel-derived WriterRef, not the plugin/payload.
	facets, err := store.GetFacets(ctx, got[0].ID)
	if err != nil || len(facets) == 0 {
		t.Fatalf("get facets: %v (n=%d)", err, len(facets))
	}
	if facets[0].Provenance.WriterRef != grant.WriterRef() {
		t.Fatalf("provenance WriterRef = %q, want channel-derived %q", facets[0].Provenance.WriterRef, grant.WriterRef())
	}
	if got[0].Labels["vcenter.name"] == "" {
		t.Fatalf("granted label not projected: %+v", got[0].Labels)
	}
}

// TestHost_CommunityCannotEmitSharedIdentity proves the tier+grant gate
// (finding #4): a community plugin's dns.fqdn is dropped even though granted; the
// Entity still syncs by its source-local vcenter.uuid, and a Rejection is logged.
func TestHost_CommunityCannotEmitSharedIdentity(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	// dns.fqdn IS granted, but the tier is community — the shared-scheme gate must
	// still refuse it.
	grant := vcenterGrant(pluginhost.TierCommunity, []string{"vcenter.uuid", "dns.fqdn"})
	client := serve(t, &fakePlugin{
		pluginID:         "vcenter-dev",
		tombstoneSchemes: []string{"vcenter.uuid"},
		entities: []*pluginv1.ObservedEntity{
			ent("u1", map[string]string{"dns.fqdn": "vm1.corp.example"}, nil),
		},
	})
	h := pluginhost.New(store, client, grant, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := h.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := h.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	got := vms(t, store)
	if len(got) != 1 {
		t.Fatalf("expected the vm to still project by its source-local id, got %d", len(got))
	}
	if _, leaked := got[0].IdentityKeys["dns.fqdn"]; leaked {
		t.Fatalf("community plugin's shared dns.fqdn must NOT be written: %+v", got[0].IdentityKeys)
	}
	if got[0].IdentityKeys["vcenter.uuid"] != "u1" {
		t.Fatalf("source-local identity should still be projected: %+v", got[0].IdentityKeys)
	}
	var sawReject bool
	for _, r := range h.Rejections() {
		if r.Kind == "identity-scheme" && r.Detail == "dns.fqdn" {
			sawReject = true
		}
	}
	if !sawReject {
		t.Fatalf("expected a recorded rejection for dns.fqdn, got %+v", h.Rejections())
	}
}

// TestHost_ManifestBeyondGrantFailsRegistration proves finding #1: a Manifest
// requesting a facet namespace outside the grant is blocking — the plugin never
// registers, never syncs.
func TestHost_ManifestBeyondGrantFailsRegistration(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	grant := vcenterGrant(pluginhost.TierTrusted, []string{"vcenter.uuid"})
	client := serve(t, &fakePlugin{
		pluginID: "vcenter-dev",
		// Requests a namespace the operator never granted — a land-grab attempt.
		contracts: []*pluginv1.ContractDecl{{SchemaId: "os.kernel", Band: "S5"}},
	})
	h := pluginhost.New(store, client, grant, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := h.Register(ctx); err == nil {
		t.Fatal("registration must fail when the manifest requests an unowned namespace")
	}
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// hostEntity is a bare ESXi-host ObservedEntity (a runs-on target).
func hostEntity(uuid string) *pluginv1.ObservedEntity {
	return &pluginv1.ObservedEntity{
		Kind: "host", IdentityKeys: map[string]string{"vcenter.host.uuid": uuid},
		Labels: map[string]string{"vcenter.name": "esxi-" + uuid},
	}
}

// TestHost_RelationsResolveByIdentity proves the ADR-0047 relations path: a vm's
// runs-on edge, named by the host's identity, is resolved and written vm->host
// (the vcenter runs-on regression from Phase B, now restored over the wire).
func TestHost_RelationsResolveByIdentity(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	grant := vcenterGrant(pluginhost.TierTrusted, []string{"vcenter.uuid", "vcenter.host.uuid"})
	grant.TombstoneSchemes = []string{"vcenter.uuid", "vcenter.host.uuid"}

	vm := ent("u1", nil, nil)
	vm.Relations = []*pluginv1.ObservedRelation{{Type: "runs-on", ToScheme: "vcenter.host.uuid", ToValue: "h1"}}
	client := serve(t, &fakePlugin{pluginID: "vcenter-dev",
		entities: []*pluginv1.ObservedEntity{hostEntity("h1"), vm}})
	h := pluginhost.New(store, client, grant, discardLog())
	if err := h.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := h.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	vmID, ok, err := store.EntityIDByIdentity(ctx, "vcenter.uuid", "u1")
	if err != nil || !ok {
		t.Fatalf("vm not projected: ok=%v err=%v", ok, err)
	}
	hostID, ok, err := store.EntityIDByIdentity(ctx, "vcenter.host.uuid", "h1")
	if err != nil || !ok {
		t.Fatalf("host not projected: ok=%v err=%v", ok, err)
	}
	targets, err := store.RelationTargets(ctx, vmID, "runs-on")
	if err != nil {
		t.Fatalf("relation targets: %v", err)
	}
	if len(targets) != 1 || targets[0] != hostID {
		t.Fatalf("runs-on edge not written vm->host: got %v want [%s]", targets, hostID)
	}
}

// TestHost_RelationTargetGated proves the target scheme is tier+grant gated: a
// relation to an UNGRANTED scheme is dropped with a rejection, never written.
func TestHost_RelationTargetGated(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	// "mac" is not in the grant — a relation targeting it must be refused.
	grant := vcenterGrant(pluginhost.TierTrusted, []string{"vcenter.uuid"})
	vm := ent("u1", nil, nil)
	vm.Relations = []*pluginv1.ObservedRelation{{Type: "peers-with", ToScheme: "mac", ToValue: "aa:bb"}}
	client := serve(t, &fakePlugin{pluginID: "vcenter-dev", entities: []*pluginv1.ObservedEntity{vm}})
	h := pluginhost.New(store, client, grant, discardLog())
	if err := h.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := h.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	vmID, _, _ := store.EntityIDByIdentity(ctx, "vcenter.uuid", "u1")
	if tg, _ := store.RelationTargets(ctx, vmID, "peers-with"); len(tg) != 0 {
		t.Fatalf("ungranted relation target must not be written, got %v", tg)
	}
	var gated bool
	for _, r := range h.Rejections() {
		if r.Kind == "relation-target" && r.Detail == "mac" {
			gated = true
		}
	}
	if !gated {
		t.Fatalf("expected a relation-target rejection for scheme mac, got %+v", h.Rejections())
	}
}

// TestHost_RelationNoVivify proves resolve-don't-vivify: a granted-scheme target
// that does not exist drops the edge and records a rejection — it NEVER creates a
// placeholder host Entity (which would covertly write an ungranted identity).
func TestHost_RelationNoVivify(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	grant := vcenterGrant(pluginhost.TierTrusted, []string{"vcenter.uuid", "vcenter.host.uuid"})
	vm := ent("u1", nil, nil)
	vm.Relations = []*pluginv1.ObservedRelation{{Type: "runs-on", ToScheme: "vcenter.host.uuid", ToValue: "ghost"}}
	client := serve(t, &fakePlugin{pluginID: "vcenter-dev", entities: []*pluginv1.ObservedEntity{vm}})
	h := pluginhost.New(store, client, grant, discardLog())
	if err := h.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := h.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	// No placeholder host was vivified.
	if _, found, _ := store.EntityIDByIdentity(ctx, "vcenter.host.uuid", "ghost"); found {
		t.Fatal("resolve-don't-vivify violated: a placeholder host Entity was created")
	}
	vmID, _, _ := store.EntityIDByIdentity(ctx, "vcenter.uuid", "u1")
	if tg, _ := store.RelationTargets(ctx, vmID, "runs-on"); len(tg) != 0 {
		t.Fatalf("edge to a missing target must be dropped, got %v", tg)
	}
	var dropped bool
	for _, r := range h.Rejections() {
		if r.Kind == "relation" {
			dropped = true
		}
	}
	if !dropped {
		t.Fatalf("expected a dropped-relation rejection, got %+v", h.Rejections())
	}
}

// TestHost_InvokeProjectsWithRunProvenance proves the Action/Invoke path: the
// plugin's Invoke returns typed outputs + a provisioned entity, and the host
// projects it with RUN provenance (distinct from the Syncer Observe path,
// ADR-0047 §2), captures the outputs, and namespace-gates provisioned creds.
func TestHost_InvokeProjectsWithRunProvenance(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	grant := vcenterGrant(pluginhost.TierTrusted, []string{"vcenter.uuid"})
	inst := &pluginv1.ObservedEntity{Kind: "vm", IdentityKeys: map[string]string{"vcenter.uuid": "created-1"}}
	client := serve(t, &fakePlugin{
		pluginID:       "vcenter-dev",
		invokeEntities: []*pluginv1.ObservedEntity{inst},
		invokeOutputs:  []byte(`{"vm":"created-1"}`),
		// second name is outside the plugin's cred namespace — must be refused.
		invokeCreds: []string{"cred/vcenter-dev/root-pw", "cred/other-src/steal"},
	})
	h := pluginhost.New(store, client, grant, discardLog())
	if err := h.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	out, err := h.Invoke(ctx, "run-1", "provision", []byte(`{}`), false)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !out.OK {
		t.Fatal("expected a terminal ok result")
	}
	if string(out.Outputs) != `{"vm":"created-1"}` {
		t.Fatalf("outputs not captured: %s", out.Outputs)
	}
	if len(out.ProvisionedEntity) != 1 {
		t.Fatalf("expected 1 provisioned entity, got %d", len(out.ProvisionedEntity))
	}
	wk, err := store.EntityWriterKind(ctx, out.ProvisionedEntity[0])
	if err != nil {
		t.Fatalf("writer kind: %v", err)
	}
	if wk != "run" {
		t.Fatalf("provisioned entity must carry RUN provenance, got %q", wk)
	}
	if len(out.ProvisionedCreds) != 1 || out.ProvisionedCreds[0] != "cred/vcenter-dev/root-pw" {
		t.Fatalf("credential namespace gate failed: %+v", out.ProvisionedCreds)
	}
	var credReject bool
	for _, r := range h.Rejections() {
		if r.Kind == "provisioned-cred" && r.Detail == "cred/other-src/steal" {
			credReject = true
		}
	}
	if !credReject {
		t.Fatalf("expected a provisioned-cred rejection, got %+v", h.Rejections())
	}
}

// TestHost_DeltaCursorPersistsAndResumes proves the host owns the delta cursor
// (ADR-0047): the first (full) sync persists the plugin's next_cursor; the second
// sync resumes from it, so the plugin returns a DELTA window whose Gone entry
// tombstones the departed entity — without a re-enumeration.
func TestHost_DeltaCursorPersistsAndResumes(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	grant := vcenterGrant(pluginhost.TierTrusted, []string{"vcenter.uuid"})
	client := serve(t, &fakePlugin{
		pluginID:    "vcenter-dev",
		entities:    []*pluginv1.ObservedEntity{ent("u1", nil, nil), ent("u2", nil, nil)},
		nextCursor:  "delta-1",
		deltaGone:   []*pluginv1.GoneEntity{{Scheme: "vcenter.uuid", Value: "u2"}},
		deltaCursor: "delta-2",
	})
	h := pluginhost.New(store, client, grant, discardLog())
	if err := h.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}

	// First sync: empty cursor → full → u1,u2 live; cursor "delta-1" persisted.
	if err := h.Sync(ctx); err != nil {
		t.Fatalf("sync1: %v", err)
	}
	if n := len(vms(t, store)); n != 2 {
		t.Fatalf("after full sync want 2 vms, got %d", n)
	}

	// Second sync: resumes from "delta-1" → the plugin returns a delta window with
	// a Gone entry for u2 → u2 tombstoned by the delta path (not a re-enumeration).
	if err := h.Sync(ctx); err != nil {
		t.Fatalf("sync2: %v", err)
	}
	got := vms(t, store)
	if len(got) != 1 || got[0].IdentityKeys["vcenter.uuid"] != "u1" {
		t.Fatalf("after delta sync want only u1 live, got %+v", got)
	}
}

// TestHost_InvokeRawOutputContractDrift proves §1.5 over the wire: a plugin that
// asserts an output contract differing from the core-pinned id is a BLOCKING
// error (drift is never silently absorbed). InvokeRaw needs no store.
func TestHost_InvokeRawOutputContractDrift(t *testing.T) {
	grant := vcenterGrant(pluginhost.TierTrusted, []string{"vcenter.uuid"})
	client := serve(t, &fakePlugin{pluginID: "vcenter-dev", invokeOutputContractID: "WRONG.output"})
	h := pluginhost.New(nil, client, grant, discardLog())
	_, err := h.InvokeRaw(context.Background(), pluginhost.ActionInvoke{
		Principal: "alice", Action: "vcenter/x", ExpectOutputContract: "actions/vcenter/x.output",
	})
	if err == nil {
		t.Fatal("plugin output-contract drift must be a blocking error (§1.5)")
	}
}

// TestHost_InvokeRawGovernsEntitiesUnprojected proves "raw ≠ ungated": InvokeRaw
// applies the tier+grant identity gate and returns only governed observations,
// UNPROJECTED (the orchestration writes once, with Run provenance). A community
// plugin's shared dns.fqdn is dropped; the source-local id survives.
func TestHost_InvokeRawGovernsEntitiesUnprojected(t *testing.T) {
	grant := vcenterGrant(pluginhost.TierCommunity, []string{"vcenter.uuid", "dns.fqdn"})
	inst := &pluginv1.ObservedEntity{Kind: "vm", IdentityKeys: map[string]string{"vcenter.uuid": "u1", "dns.fqdn": "vm1.corp"}}
	client := serve(t, &fakePlugin{pluginID: "vcenter-dev", invokeEntities: []*pluginv1.ObservedEntity{inst}})
	h := pluginhost.New(nil, client, grant, discardLog())
	raw, err := h.InvokeRaw(context.Background(), pluginhost.ActionInvoke{Principal: "alice", Action: "vcenter/x"})
	if err != nil {
		t.Fatalf("invokeRaw: %v", err)
	}
	if !raw.OK {
		t.Fatal("expected terminal ok")
	}
	if len(raw.Entities) != 1 {
		t.Fatalf("want 1 governed entity, got %d", len(raw.Entities))
	}
	if _, leaked := raw.Entities[0].IdentityKeys["dns.fqdn"]; leaked {
		t.Fatalf("community plugin's shared dns.fqdn must be gated out: %+v", raw.Entities[0].IdentityKeys)
	}
	if raw.Entities[0].IdentityKeys["vcenter.uuid"] != "u1" {
		t.Fatalf("source-local identity must survive: %+v", raw.Entities[0].IdentityKeys)
	}
	var gated bool
	for _, r := range raw.Rejections {
		if r.Kind == "identity-scheme" && r.Detail == "dns.fqdn" {
			gated = true
		}
	}
	if !gated {
		t.Fatalf("expected a dns.fqdn rejection, got %+v", raw.Rejections)
	}
}

// TestHost_TombstoneAbsentOnFullSync proves liveness crosses the wire
// (ADR-0042): an Entity absent from a later full sync is tombstoned.
func TestHost_TombstoneAbsentOnFullSync(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	grant := vcenterGrant(pluginhost.TierTrusted, []string{"vcenter.uuid"})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	first := serve(t, &fakePlugin{pluginID: "vcenter-dev", tombstoneSchemes: []string{"vcenter.uuid"},
		entities: []*pluginv1.ObservedEntity{ent("u1", nil, nil), ent("u2", nil, nil)}})
	h1 := pluginhost.New(store, first, grant, log)
	if err := h1.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := h1.Sync(ctx); err != nil {
		t.Fatalf("sync1: %v", err)
	}
	if n := len(vms(t, store)); n != 2 {
		t.Fatalf("after sync1 want 2 vms, got %d", n)
	}

	// Second full sync reports only u1 → u2 must tombstone (leave the live set).
	second := serve(t, &fakePlugin{pluginID: "vcenter-dev", tombstoneSchemes: []string{"vcenter.uuid"},
		entities: []*pluginv1.ObservedEntity{ent("u1", nil, nil)}})
	h2 := pluginhost.New(store, second, grant, log)
	if err := h2.Register(ctx); err != nil {
		t.Fatalf("register2: %v", err)
	}
	if err := h2.Sync(ctx); err != nil {
		t.Fatalf("sync2: %v", err)
	}
	got := vms(t, store)
	if len(got) != 1 || got[0].IdentityKeys["vcenter.uuid"] != "u1" {
		t.Fatalf("after sync2 want only u1 live, got %+v", got)
	}
}
