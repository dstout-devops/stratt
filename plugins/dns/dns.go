// Package dns is the DNS Connector/Actuator behind the sovereign plugin port
// (ADR-0144): the provider `Intent/DnsRecord` never had. It closes the LAST of
// ADDR-1's reach-coordinate producers — the REGISTERED one, where the estate
// declares a name, this plugin creates the record, and the coordinate is then a
// fact Stratt CAUSED rather than one it assumed.
//
// Three verbs, one job:
//
//   - OBSERVE — AXFR the declared zones and project what is actually there. This is
//     the SOLE producer of mgmt.address here (ADR-0144 D5): a build asserts what it
//     INTENDED, a zone read reports what is TRUE, and only the second is a projection
//     (§1.2). DNS stays the system of record for names; the graph is rebuildable from it.
//   - INVOKE — `dns/register` / `dns/deregister`, the targetless Actions an
//     Intent/DnsRecord's gated build Workflow launches. The estate declares the record;
//     the provider writes it.
//   - APPLY — the fleet half. "Every machine in this View has a name in this zone",
//     where the record's data is NOT declared but is each target's own reach coordinate,
//     which the core resolved from the graph and handed over as ApplyTarget.address.
//     That is the only way a plugin can learn an address (the port carries no facets
//     per target), and it is why no IP appears in Git.
//
// The transport is RFC 2136 dynamic update + AXFR (ADR-0144 D2), because it is the one
// update mechanism that belongs to no vendor: BIND, Knot, PowerDNS, Infoblox and BlueCat
// all speak it. It is a transport BENEATH our contract (§1.5), not the contract — a
// cloud-API DNS provider is a sibling behind the same Intent and the same capability
// class. Windows AD DNS is NOT covered: it accepts secure updates only over GSS-TSIG,
// which this provider does not speak, and saying so is better than a provider that
// half-works against the estate an enterprise actually runs.
package dns

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/dstout-devops/stratt/sdk/pluginserve"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// The plugin's OWN contract documents (ADR-0138 D3/D4), embedded so their digests ride
// every ContractRef — the port's invariant #5. Core learns these from the estate's
// admission of this plugin's tree, never from the Manifest (which carries an id and a
// hash, never a schema).
//
//go:embed contracts
var contractFS embed.FS

var contracts = pluginserve.Contracts(contractFS)

const protocolVersion = "v1"

// The targetless Actions this plugin serves over Invoke.
const (
	actionRegister   = "dns/register"
	actionDeregister = "dns/deregister"
)

// Config locates the authoritative DNS server this plugin drives and reads.
type Config struct {
	// PluginID is the authenticated channel identity the operator grant is keyed on.
	PluginID string
	// Server is the authoritative server's host:port for UPDATE and AXFR alike — one
	// coordinate, because a provider that wrote to one server and read from another
	// could report a record it did not create (§1.2).
	Server string
	// Zones are the zones this Syncer enumerates. Explicit, never discovered: AXFR is
	// a whole-zone read and "which zones are ours" is an operator's statement about
	// their estate, not something to infer from a server.
	Zones []string
	// ProjectKind is the Entity kind the names in these zones DENOTE (default `host`).
	// The operator's call, not the plugin's, and not cosmetic: upsertEntityTx SETS
	// `kind` whenever a projection correlates onto an existing Entity, so a Syncer
	// that guessed would silently retype another source's Entity — a `vm` becoming a
	// `dns-record` and dropping out of every View that selects vms. A zone is an
	// estate's naming domain, so the estate declares what its names are.
	ProjectKind string
	// TSIG is the shared key this plugin signs UPDATE and AXFR with. Material reaches
	// the pod the way every other plugin credential does (a mounted Secret → env), and
	// never appears in a declaration, a log, or the graph (§2.5).
	TSIG TSIGKey
}

// Server implements the sovereign plugin port for the DNS plugin. It holds no graph
// write path (§1.2): Observe proposes ObservedEntities and the core-side host governs
// the write.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg  Config
	zone zoneClient // injectable — the unit suite drives a fake zone with no server
	log  *slog.Logger
}

func NewServer(cfg Config, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "dns"
	}
	return &Server{cfg: cfg, zone: &rfc2136{server: cfg.Server, key: cfg.TSIG}, log: log.With("plugin", "dns")}
}

// WithZoneClient swaps the transport — the unit tests' seam, and the reason the
// projection rules (normalize.go) are testable without a DNS server anywhere.
func (s *Server) WithZoneClient(z zoneClient) *Server { s.zone = z; return s }

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: protocolVersion,
		// SYNCER class: the graph-facing job is the projection, and the Actions exist to
		// make something true that the projection then reports. Same shape as vcenter,
		// which is a Syncer that also builds VMs.
		Class: pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs: []pluginv1.Verb{
			pluginv1.Verb_VERB_OBSERVE, pluginv1.Verb_VERB_INVOKE, pluginv1.Verb_VERB_APPLY,
		},
		// mgmt.address ONLY. `dns.record` is registered owned-but-uncovered (ADR-0059) and
		// stays that way: §1.1 forbids a Facet schema no shipping Contract demands, and
		// nothing consumes a zone read-model. Projecting the whole zone as graph state
		// would be building a second DNS rather than observing the first.
		Contracts: []*pluginv1.ContractDecl{{SchemaId: "mgmt.address"}},
		// NO TombstoneSchemes, deliberately, and the reason is worth keeping: the only
		// identity this plugin emits is dns.fqdn, which it SHARES with `declared` and
		// `vcenter` — correlating onto their Entities is the entire mechanism (ADR-0144
		// D3). Granting a tombstone scheme on a shared identity would let a zone that
		// stopped mentioning a name tombstone the vCenter VM behind it. The cost is
		// stated in the ADR: a removed record's coordinate lingers, because core has no
		// facet-retraction path at all.
		Capabilities: []string{"provisioning"},
		Actions: []*pluginv1.ActionDecl{
			{
				Name:   actionRegister,
				Input:  contracts.Ref("actions/dns/register.input"),
				Output: contracts.Ref("actions/dns/register.output"),
				// Idempotent: an UPDATE that removes the RRset and re-adds it converges to
				// the same zone state however many times it runs (RFC 2136 §2.5.2 + §2.5.1).
				Idempotent:  true,
				DryRunnable: true,
			},
			{
				Name:        actionDeregister,
				Input:       contracts.Ref("actions/dns/deregister.input"),
				Output:      contracts.Ref("actions/dns/deregister.output"),
				Idempotent:  true, // removing an absent RRset is a no-op success
				DryRunnable: true,
			},
		},
		MinProtocol: protocolVersion,
		MaxProtocol: protocolVersion,
	}}, nil
}

func (s *Server) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	return &pluginv1.HealthResponse{Status: pluginv1.HealthResponse_SERVING_UP, ProtocolVersion: protocolVersion}, nil
}

// Observe enumerates every declared zone by AXFR and streams the projection. A full
// enumeration each poll: AXFR has no delta form, and pretending otherwise would mean
// inventing a cursor over a protocol that has none.
//
// FullSyncComplete is honest — the whole SoR was read — and it is safe because the
// Manifest grants no tombstone scheme, so the core host tombstones nothing.
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	ctx := stream.Context()
	if len(s.cfg.Zones) == 0 {
		return status.Error(codes.FailedPrecondition, "dns: no zones configured — this Syncer enumerates DECLARED zones (STRATT_DNS_ZONES); it never discovers them")
	}
	var entities []*pluginv1.ObservedEntity
	for _, z := range s.cfg.Zones {
		records, err := s.zone.Transfer(ctx, z)
		if err != nil {
			// A zone that fails to transfer fails the poll rather than yielding a
			// partial projection that would look like "those names are gone" (§1.8).
			return status.Errorf(codes.Unavailable, "dns: transfer zone %q: %v", z, err)
		}
		ents, skipped := normalizeZone(s.cfg.ProjectKind, records)
		s.log.Info("zone enumerated", "zone", z, "records", len(records), "projected", len(ents), "not_a_coordinate", skipped)
		entities = append(entities, ents...)
	}
	return stream.Send(&pluginv1.ObserveResponse{
		Entities:         entities,
		FullSync:         true,
		FullSyncComplete: true,
	})
}

// registerArgs is the opaque `args` payload of dns/register — the plugin's own shape,
// typed by contracts/actions/dns/register.input and never by core (§1.1).
type registerArgs struct {
	Zone string `json:"zone"`
	Name string `json:"name"`
	Type string `json:"type"`
	Data string `json:"data"`
	TTL  uint32 `json:"ttl,omitempty"`
	// ProjectKind / Labels ride the Intent's build launch (ADR-0059 §6) so the record
	// projects back correlated to its own Finding. Identity-only, no Facet: an Action's
	// write-back is entity-only by construction, and mgmt.address is the Syncer's (D5).
	ProjectKind string            `json:"projectKind,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// deregisterArgs is dns/deregister's payload: an RRset is removed by (name, type),
// never by value — removing "the A record whose data is X" would silently do nothing
// when the data has drifted, which is exactly when removal matters.
type deregisterArgs struct {
	Zone string `json:"zone"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// Invoke serves the targetless Actions. The build path: an Intent/DnsRecord resolves to
// this provider, its gated Workflow launches dns/register with the declared record, and
// the record projects back as a dns-record Entity carrying the singleton correlation
// label so the provisioning Finding resolves.
func (s *Server) Invoke(req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	ctx := stream.Context()
	cid := req.GetEnvelope().GetCorrelationId()
	switch action := req.GetAction(); action {
	case actionRegister, "":
		return s.invokeRegister(ctx, stream, cid, req)
	case actionDeregister:
		return s.invokeDeregister(ctx, stream, cid, req)
	default:
		return status.Errorf(codes.InvalidArgument, "dns: unknown action %q (%q or %q)", action, actionRegister, actionDeregister)
	}
}

func (s *Server) invokeRegister(ctx context.Context, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], cid string, req *pluginv1.InvokeRequest) error {
	var a registerArgs
	if err := json.Unmarshal(req.GetArgs().GetBytes(), &a); err != nil {
		return invokeFailed(stream, cid, fmt.Errorf("invalid args: %w", err))
	}
	rec, err := newRecord(a.Zone, a.Name, a.Type, a.Data, a.TTL)
	if err != nil {
		return invokeFailed(stream, cid, err)
	}
	if req.GetDryRun() {
		return invokeOK(stream, cid, fmt.Sprintf("dry-run ok: would write %s", rec), nil, nil)
	}
	if err := s.zone.Update(ctx, a.Zone, rec); err != nil {
		return invokeFailed(stream, cid, fmt.Errorf("register %s: %w", rec, err))
	}
	outputs, err := json.Marshal(map[string]any{"name": rec.Name, "type": rec.Type, "data": rec.Data, "ttl": rec.TTL})
	if err != nil {
		return invokeFailed(stream, cid, err)
	}
	// The project-back (ADR-0059 §6): identity + labels only. The record's own name is
	// its dns.fqdn — for an A record that is also the canonical name, which is what makes
	// the Syncer's later projection land on the same Entity (ADR-0144 D3).
	kind := a.ProjectKind
	if kind == "" {
		kind = "dns-record"
	}
	ent := &pluginv1.ObservedEntity{
		Kind:         kind,
		IdentityKeys: map[string]string{"dns.fqdn": rec.Name},
		Labels:       a.Labels,
	}
	return invokeOK(stream, cid, fmt.Sprintf("registered %s", rec), outputs, []*pluginv1.ObservedEntity{ent})
}

func (s *Server) invokeDeregister(ctx context.Context, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], cid string, req *pluginv1.InvokeRequest) error {
	var a deregisterArgs
	if err := json.Unmarshal(req.GetArgs().GetBytes(), &a); err != nil {
		return invokeFailed(stream, cid, fmt.Errorf("invalid args: %w", err))
	}
	name, err := canonical(a.Zone, a.Name)
	if err != nil {
		return invokeFailed(stream, cid, err)
	}
	rtype, err := supportedType(a.Type)
	if err != nil {
		return invokeFailed(stream, cid, err)
	}
	if req.GetDryRun() {
		return invokeOK(stream, cid, fmt.Sprintf("dry-run ok: would remove %s %s", name, rtype), nil, nil)
	}
	if err := s.zone.Remove(ctx, a.Zone, name, rtype); err != nil {
		return invokeFailed(stream, cid, fmt.Errorf("deregister %s %s: %w", name, rtype, err))
	}
	outputs, err := json.Marshal(map[string]any{"name": name, "type": rtype})
	if err != nil {
		return invokeFailed(stream, cid, err)
	}
	return invokeOK(stream, cid, fmt.Sprintf("removed %s %s", name, rtype), outputs, nil)
}

// applyParams is the opaque `desired` payload of the fleet path, typed by
// contracts/actuators/dns.input. There is no `data` field, and its absence IS the
// design: the record's data is each TARGET's own coordinate, which the core resolved
// from the graph. A `data` here would be one value for a whole View.
type applyParams struct {
	Zone string `json:"zone"`
	TTL  uint32 `json:"ttl,omitempty"`
}

// Apply registers one record per target: `<target name>.<zone>` pointing at that
// target's OWN reach coordinate. An address ⇒ an A/AAAA; a name ⇒ a CNAME to it. That
// branch is a fact about the value in hand, not a policy — and it is what lets the
// estate's stable name alias whatever the substrate currently calls the machine.
//
// A target with no coordinate is REPORTED AND SKIPPED, never guessed at. Registration
// binds a name to a coordinate the substrate already produced; it cannot conjure
// reachability where none is observed (ADR-0144 D1), and a name that resolves nowhere
// is worse than an absent one (§1.8).
func (s *Server) Apply(req *pluginv1.ApplyRequest, stream grpc.ServerStreamingServer[pluginv1.ApplyResponse]) error {
	ctx := stream.Context()
	var p applyParams
	if err := json.Unmarshal(req.GetDesired().GetBytes(), &p); err != nil {
		return pluginserve.ApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, fmt.Sprintf("invalid params: %v", err))
	}
	if strings.TrimSpace(p.Zone) == "" {
		return pluginserve.ApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "zone is required")
	}
	targets := req.GetTargets()
	if len(targets) == 0 {
		// Not an error: an empty View is a converged View. Saying so beats a silent OK.
		return pluginserve.ApplyTerminal(stream, true, pluginv1.ItemResult_STATUS_OK, "no targets in view — nothing to register")
	}

	emit := func(t *pluginv1.ApplyTarget, level pluginv1.TaskEvent_Level, st pluginv1.ItemResult_Status, msg string) {
		_ = stream.Send(&pluginv1.ApplyResponse{
			Event: &pluginv1.TaskEvent{
				Level: level, At: timestamppb.Now(), Message: msg,
				Scope:  pluginv1.TaskEvent_SCOPE_TASK,
				Fields: map[string]string{"target": t.GetName()},
			},
			Result: &pluginv1.ItemResult{ItemKey: t.GetName(), Status: st},
		})
	}

	changed, skipped, failed := 0, 0, 0
	for _, t := range targets {
		addr := strings.TrimSpace(t.GetAddress())
		if addr == "" {
			skipped++
			// STATUS_OK, not a failure: a machine with no coordinate yet is a REAL,
			// visible state (ADR-0143 D1), not a broken registration. The WARN line and
			// the terminal count are what make it legible — a silent OK would be the
			// §1.8 defect, and inventing a SKIPPED status the port does not have would
			// be worse than saying so plainly.
			emit(t, pluginv1.TaskEvent_LEVEL_WARN, pluginv1.ItemResult_STATUS_OK,
				fmt.Sprintf("%s has no reach coordinate — nothing to bind a name to; registration cannot create reachability, only name it", t.GetName()))
			continue
		}
		rec, err := recordForTarget(p.Zone, t.GetName(), addr, p.TTL)
		if err != nil {
			failed++
			emit(t, pluginv1.TaskEvent_LEVEL_ERROR, pluginv1.ItemResult_STATUS_FAILED, err.Error())
			continue
		}
		if req.GetDryRun() {
			emit(t, pluginv1.TaskEvent_LEVEL_INFO, pluginv1.ItemResult_STATUS_OK, "dry-run: would write "+rec.String())
			continue
		}
		if err := s.zone.Update(ctx, p.Zone, rec); err != nil {
			failed++
			emit(t, pluginv1.TaskEvent_LEVEL_ERROR, pluginv1.ItemResult_STATUS_FAILED, fmt.Sprintf("register %s: %v", rec, err))
			continue
		}
		changed++
		emit(t, pluginv1.TaskEvent_LEVEL_INFO, pluginv1.ItemResult_STATUS_CHANGED, "registered "+rec.String())
	}

	msg := fmt.Sprintf("zone %s: %d registered, %d skipped (no coordinate), %d failed", p.Zone, changed, skipped, failed)
	if failed > 0 {
		return pluginserve.ApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, msg)
	}
	st := pluginv1.ItemResult_STATUS_OK
	if changed > 0 {
		st = pluginv1.ItemResult_STATUS_CHANGED
	}
	return pluginserve.ApplyTerminal(stream, true, st, msg)
}

func invokeOK(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], cid, msg string, outputs []byte, entities []*pluginv1.ObservedEntity) error {
	res := &pluginv1.InvokeResult{Entities: entities}
	if outputs != nil {
		res.Outputs = &pluginv1.Payload{Bytes: outputs}
		res.OutputContract = contracts.Ref("actions/dns/register.output")
	}
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(), CorrelationId: cid,
			Terminal: true, Ok: true, Message: msg, Fields: map[string]string{"kind": "finished"},
		},
		Result: res,
	})
}

// invokeFailed emits the terminal not-ok InvokeResponse — a domain failure rides the
// typed descent channel (§1.8), never a bare transport error.
func invokeFailed(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], cid string, cause error) error {
	return stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_ERROR, At: timestamppb.Now(), CorrelationId: cid,
		Terminal: true, Ok: false, Message: cause.Error(), Fields: map[string]string{"kind": "finished"},
	}})
}
