package pluginhost_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/planstore"
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
	subscribeEvents        []*pluginv1.EmittedEvent
	// delta-cursor knobs: on an empty-cursor (full) Observe, NextCursor=nextCursor;
	// on a cursored (delta) Observe, emit deltaGone + NextCursor=deltaCursor.
	nextCursor  string
	deltaGone   []*pluginv1.GoneEntity
	deltaCursor string
	// apply knobs: applyStream is sent verbatim (per-target results, write-back,
	// drift, derived contracts, the terminal event) so a test drives the host's
	// core-side fold / confused-deputy / write-back governance precisely.
	applyStream    []*pluginv1.ApplyResponse
	captureApplyIn func(*pluginv1.ApplyRequest)
	planResp       *pluginv1.PlanResponse
}

func (f *fakePlugin) Plan(_ context.Context, _ *pluginv1.PlanRequest) (*pluginv1.PlanResponse, error) {
	if f.planResp != nil {
		return f.planResp, nil
	}
	return &pluginv1.PlanResponse{}, nil
}

// memArtifactDB is an in-memory, write-once planstore.ArtifactDB for host tests.
type memArtifactDB struct{ m map[string][]byte }

func (d *memArtifactDB) PutPlanArtifact(_ context.Context, sha string, ct []byte) error {
	if d.m == nil {
		d.m = map[string][]byte{}
	}
	if _, ok := d.m[sha]; !ok {
		d.m[sha] = append([]byte(nil), ct...)
	}
	return nil
}

func (d *memArtifactDB) GetPlanArtifact(_ context.Context, sha string) ([]byte, error) {
	ct, ok := d.m[sha]
	if !ok {
		return nil, planstore.ErrNotFound
	}
	return ct, nil
}

func (f *fakePlugin) Apply(req *pluginv1.ApplyRequest, stream grpc.ServerStreamingServer[pluginv1.ApplyResponse]) error {
	if f.captureApplyIn != nil {
		f.captureApplyIn(req)
	}
	for _, r := range f.applyStream {
		if err := stream.Send(r); err != nil {
			return err
		}
	}
	return nil
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

func (f *fakePlugin) Subscribe(_ *pluginv1.SubscribeRequest, stream grpc.ServerStreamingServer[pluginv1.SubscribeResponse]) error {
	for _, ev := range f.subscribeEvents {
		if err := stream.Send(&pluginv1.SubscribeResponse{Event: ev}); err != nil {
			return err
		}
	}
	<-stream.Context().Done() // hold the stream open until the host cancels
	return stream.Context().Err()
}

// capturePub captures published emitter events for assertions.
type capturePub struct{ ch chan types.EmitterEvent }

func (c *capturePub) PublishEmitterEvent(_ context.Context, ev types.EmitterEvent) error {
	c.ch <- ev
	return nil
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

// TestHost_SubscribeGrantBoundEmitterName proves the guardian anti-spoof
// invariant: the published EmitterEvent.Emitter is the GRANT's emitter name — not
// the plugin's subject/type — the legible `match` becomes the CEL payload, and an
// event with no match projection is dropped VISIBLY (§1.8), never silently.
func TestHost_SubscribeGrantBoundEmitterName(t *testing.T) {
	grant := vcenterGrant(pluginhost.TierTrusted, []string{"vcenter.uuid"})
	grant.EmitterName = "salt-prod"
	m, _ := structpb.NewStruct(map[string]any{"tag": "salt/job/x", "data": map[string]any{"fun": "test.ping"}})
	spoofed := &pluginv1.EmittedEvent{Subject: "attacker", Type: "impersonate", Match: m}
	malformed := &pluginv1.EmittedEvent{} // no match projection
	client := serve(t, &fakePlugin{pluginID: grant.PluginIdentity,
		subscribeEvents: []*pluginv1.EmittedEvent{spoofed, malformed}})
	h := pluginhost.New(nil, client, grant, discardLog())
	pub := &capturePub{ch: make(chan types.EmitterEvent, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.SubscribeLoop(ctx, pub) }()

	got := <-pub.ch // the spoofed event's published form
	if got.Emitter != "salt-prod" {
		t.Fatalf("emitter must be the grant name, got %q (plugin subject/type must not route)", got.Emitter)
	}
	if got.Payload["tag"] != "salt/job/x" {
		t.Fatalf("CEL payload must be the legible match projection: %+v", got.Payload)
	}
	cancel()
	<-done

	var dropped bool
	for _, r := range h.Rejections() {
		if r.Kind == "emitter" {
			dropped = true
		}
	}
	if !dropped {
		t.Fatalf("the match-less event must be dropped with a visible rejection, got %+v", h.Rejections())
	}
}

// TestHost_SubscribeIdentityBinding proves emission is bound to the authenticated
// channel identity: a plugin whose manifest identity != the granted identity is
// refused before any event flows (anti-spoof).
func TestHost_SubscribeIdentityBinding(t *testing.T) {
	grant := vcenterGrant(pluginhost.TierTrusted, []string{"vcenter.uuid"})
	client := serve(t, &fakePlugin{pluginID: "IMPOSTER"}) // != grant.PluginIdentity
	h := pluginhost.New(nil, client, grant, discardLog())
	pub := &capturePub{ch: make(chan types.EmitterEvent, 1)}
	if err := h.SubscribeLoop(context.Background(), pub); err == nil {
		t.Fatal("a plugin whose manifest identity != the grant identity must be refused (anti-spoof)")
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

// applyEvent is a small helper for building the streamed ApplyResponses.
func applyResult(target string, st pluginv1.ItemResult_Status) *pluginv1.ApplyResponse {
	return &pluginv1.ApplyResponse{Result: &pluginv1.ItemResult{ItemKey: target, Status: st}}
}

// TestHost_ApplyCoreSideFold proves guardian fix #1 (targets reach the plugin
// LEGIBLY, never in the opaque payload) and fix #3 (Succeeded is folded core-side
// from per-target statuses — a plugin's terminal ok=true alongside a FAILED target
// still yields a non-OK Run, §1.8). ApplyRaw needs no store (nothing projected).
func TestHost_ApplyCoreSideFold(t *testing.T) {
	grant := vcenterGrant(pluginhost.TierTrusted, []string{"vcenter.uuid"})
	var captured *pluginv1.ApplyRequest
	fp := &fakePlugin{
		pluginID:       grant.PluginIdentity,
		captureApplyIn: func(r *pluginv1.ApplyRequest) { captured = r },
		applyStream: []*pluginv1.ApplyResponse{
			{Event: &pluginv1.TaskEvent{Level: pluginv1.TaskEvent_LEVEL_INFO, Message: "applying"}},
			applyResult("web-1", pluginv1.ItemResult_STATUS_CHANGED),
			applyResult("web-2", pluginv1.ItemResult_STATUS_FAILED),
			// The lie: the plugin self-asserts success on the terminal event.
			{Event: &pluginv1.TaskEvent{Terminal: true, Ok: true}},
		},
	}
	client := serve(t, fp)
	h := pluginhost.New(nil, client, grant, discardLog())
	raw, err := h.ApplyRaw(context.Background(), pluginhost.ApplyInvoke{
		Principal: "alice",
		Params:    []byte(`{"module":"nginx"}`),
		Targets: []pluginhost.ApplyTarget{
			{Name: "web-1", IdentityKeys: map[string]string{"vcenter.uuid": "u1"}, Vars: map[string]string{"ansible_host": "10.0.0.1"}},
			{Name: "web-2", IdentityKeys: map[string]string{"vcenter.uuid": "u2"}},
		},
	})
	if err != nil {
		t.Fatalf("applyRaw: %v", err)
	}
	// Fix #3: the plugin's terminal ok is NOT trusted — a FAILED target is non-OK.
	if raw.Succeeded {
		t.Fatal("a FAILED target must fold to a non-OK Run regardless of the plugin's terminal ok (§1.8)")
	}
	if raw.PerTarget["web-1"] != "changed" || raw.PerTarget["web-2"] != "failed" {
		t.Fatalf("per-target fold wrong: %+v", raw.PerTarget)
	}
	// Fix #1: the target set crossed the wire LEGIBLY (typed field, not the payload).
	if captured == nil || len(captured.GetTargets()) != 2 {
		t.Fatalf("targets must reach the plugin as a legible field, got %+v", captured.GetTargets())
	}
	if captured.GetTargets()[0].GetName() != "web-1" || captured.GetTargets()[0].GetVars()["ansible_host"] != "10.0.0.1" {
		t.Fatalf("legible target detail lost: %+v", captured.GetTargets()[0])
	}
	if string(captured.GetDesired().GetBytes()) != `{"module":"nginx"}` {
		t.Fatalf("opaque desired must carry the tool config verbatim: %q", captured.GetDesired().GetBytes())
	}
}

// TestHost_ApplyConfusedDeputyRejectsUnresolvedTarget proves guardian fix #1's
// gate: a per-target status keyed to a target OUTSIDE the Step's resolved set is
// REJECTED visibly (§1.8), never folded into the outcome — a plugin cannot report
// status against a target the core did not authorize for this Step.
func TestHost_ApplyConfusedDeputyRejectsUnresolvedTarget(t *testing.T) {
	grant := vcenterGrant(pluginhost.TierTrusted, []string{"vcenter.uuid"})
	fp := &fakePlugin{
		pluginID: grant.PluginIdentity,
		applyStream: []*pluginv1.ApplyResponse{
			applyResult("web-1", pluginv1.ItemResult_STATUS_OK),
			applyResult("db-secret", pluginv1.ItemResult_STATUS_FAILED), // NOT in the resolved set
			{Event: &pluginv1.TaskEvent{Terminal: true, Ok: true}},
		},
	}
	client := serve(t, fp)
	h := pluginhost.New(nil, client, grant, discardLog())
	raw, err := h.ApplyRaw(context.Background(), pluginhost.ApplyInvoke{
		Principal: "alice",
		Targets:   []pluginhost.ApplyTarget{{Name: "web-1"}},
	})
	if err != nil {
		t.Fatalf("applyRaw: %v", err)
	}
	if _, leaked := raw.PerTarget["db-secret"]; leaked {
		t.Fatalf("out-of-scope target must never enter the outcome: %+v", raw.PerTarget)
	}
	var rejected bool
	for _, r := range raw.Rejections {
		if r.Kind == "item-result" && r.Detail == "db-secret" {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("expected a confused-deputy rejection for db-secret, got %+v", raw.Rejections)
	}
}

// TestHost_ApplyWriteBackGovernedUnprojected proves apply write-back is "raw ≠
// ungated": the identity-scheme tier+grant gate applies exactly as on the Syncer
// path, ungranted schemes are dropped with a Rejection, and the governed set is
// returned UNPROJECTED (store is nil — the orchestration writes once with Run
// provenance, guardian fix #2).
func TestHost_ApplyWriteBackGovernedUnprojected(t *testing.T) {
	grant := vcenterGrant(pluginhost.TierCommunity, []string{"vcenter.uuid", "dns.fqdn"})
	fp := &fakePlugin{
		pluginID: grant.PluginIdentity,
		applyStream: []*pluginv1.ApplyResponse{
			{WriteBack: []*pluginv1.ObservedEntity{{
				Kind:         "vm",
				IdentityKeys: map[string]string{"vcenter.uuid": "u9", "dns.fqdn": "vm9.corp"},
				Labels:       map[string]string{"vcenter.name": "vm9"},
				Facets:       map[string][]byte{"vm.config": []byte(`{"cpus":4}`)},
			}}},
			{Event: &pluginv1.TaskEvent{Terminal: true, Ok: true}},
		},
	}
	client := serve(t, fp)
	h := pluginhost.New(nil, client, grant, discardLog())
	raw, err := h.ApplyRaw(context.Background(), pluginhost.ApplyInvoke{Principal: "alice"})
	if err != nil {
		t.Fatalf("applyRaw: %v", err)
	}
	if len(raw.WriteBack) != 1 {
		t.Fatalf("want 1 governed write-back entity, got %d", len(raw.WriteBack))
	}
	wb := raw.WriteBack[0]
	if _, leaked := wb.IdentityKeys["dns.fqdn"]; leaked {
		t.Fatalf("community plugin's shared dns.fqdn must be gated out of write-back: %+v", wb.IdentityKeys)
	}
	if wb.IdentityKeys["vcenter.uuid"] != "u9" {
		t.Fatalf("source-local identity must survive: %+v", wb.IdentityKeys)
	}
	if string(wb.Facets["vm.config"]) != `{"cpus":4}` {
		t.Fatalf("granted facet must survive write-back: %+v", wb.Facets)
	}
}

const planTestKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

// TestHost_PlanContentAddressesSavedPlan proves the Plan verb: the CORE computes
// the sha256 of the saved plan (a plugin-asserted plan.sha256 is advisory),
// encrypts + stores it, and returns that digest as the pin (ADR-0047 §8).
func TestHost_PlanContentAddressesSavedPlan(t *testing.T) {
	grant := vcenterGrant(pluginhost.TierTrusted, []string{"vcenter.uuid"})
	saved := []byte("SAVED-TOFU-PLAN-with-secret-hunter2")
	fp := &fakePlugin{pluginID: grant.PluginIdentity, planResp: &pluginv1.PlanResponse{
		Summary:   "plan for prod",
		SavedPlan: saved,
		Plan:      &pluginv1.ArtifactRef{Sha256: "LIES-plugin-asserted-hash"}, // advisory, ignored
	}}
	db := &memArtifactDB{}
	ps, err := planstore.New(planTestKey, db)
	if err != nil {
		t.Fatalf("planstore: %v", err)
	}
	h := pluginhost.New(nil, serve(t, fp), grant, discardLog()).UsePlanStore(ps)
	out, err := h.Plan(context.Background(), pluginhost.PlanInvoke{Principal: "alice", Params: []byte(`{"workspace":"prod"}`)})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	sum := sha256.Sum256(saved)
	want := hex.EncodeToString(sum[:])
	if out.Digest != want {
		t.Fatalf("core must content-address the saved plan itself, got %q want %q (plugin's asserted hash must be ignored)", out.Digest, want)
	}
	// Stored encrypted — the plan secret must not sit in the clear.
	if bytes.Contains(db.m[want], []byte("hunter2")) {
		t.Fatal("saved plan must be encrypted at rest (§2.5)")
	}
	// GetVerified round-trips the exact plan.
	got, err := h.VerifyPinnedPlan(context.Background(), out.Digest)
	if err != nil || !bytes.Equal(got, saved) {
		t.Fatalf("verify pinned plan: %v (%q)", err, got)
	}
}

// TestHost_ApplyPinnedPlanCrossesTheWire proves a Gate-approved pinned plan reaches
// the plugin as bytes + plan_ref (the plugin applies EXACTLY it, ADR-0047 §8).
func TestHost_ApplyPinnedPlanCrossesTheWire(t *testing.T) {
	grant := vcenterGrant(pluginhost.TierTrusted, []string{"vcenter.uuid"})
	var captured *pluginv1.ApplyRequest
	fp := &fakePlugin{pluginID: grant.PluginIdentity,
		captureApplyIn: func(r *pluginv1.ApplyRequest) { captured = r },
		applyStream:    []*pluginv1.ApplyResponse{{Event: &pluginv1.TaskEvent{Terminal: true, Ok: true}, Result: &pluginv1.ItemResult{Status: pluginv1.ItemResult_STATUS_CHANGED}}},
	}
	h := pluginhost.New(nil, serve(t, fp), grant, discardLog())
	_, err := h.ApplyRaw(context.Background(), pluginhost.ApplyInvoke{
		Principal: "alice", PlanDigest: "abc123", PinnedPlan: []byte("PINNED-PLAN-BYTES"),
	})
	if err != nil {
		t.Fatalf("applyRaw: %v", err)
	}
	if captured == nil || string(captured.GetPinnedPlan()) != "PINNED-PLAN-BYTES" {
		t.Fatalf("pinned plan bytes must reach the plugin: %+v", captured.GetPinnedPlan())
	}
	if captured.GetPlanRef().GetSha256() != "abc123" {
		t.Fatalf("plan_ref digest must reach the plugin, got %q", captured.GetPlanRef().GetSha256())
	}
}
