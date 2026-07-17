package certissuer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// facetNamespaces are the Facet namespaces this Syncer REQUESTS to own (§2.1); the
// core honors them only where the operator grant allows.
var facetNamespaces = []string{
	"cert.identity", // commonName, serialNumber, issuer, dnsNames
	"cert.expiry",   // notBefore, notAfter
}

// Config locates the CLM Source. The token is a spawn-time CredentialRef resolved
// from the plugin's OWN broker (dev: STRATT_CLM_TOKEN); material never crosses the
// core and is never echoed (§2.5, §1.8).
type Config struct {
	PluginID string // the authenticated channel identity the operator grant is keyed on
	Addr     string // CLM base URL (dev: OpenBao on :8200)
	Token    string // X-Vault-Token; read + write credential
	Mount    string // PKI secrets-engine mount (default "pki")
}

// Server implements the sovereign plugin port for the certissuer Connector — a
// multi-role plugin advertising OBSERVE (the cert Syncer) AND the reconcile
// ACTUATOR verbs PLAN/APPLY/DESTROY (cert lifecycle, ADR-0050). It advertises the
// facet namespaces + tombstone scheme it REQUESTS to own; the core-side host honors
// them only where the operator grant allows. The plugin holds no graph write path
// (§1.2) — it proposes typed values, the host stamps provenance + validates.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg Config
	log *slog.Logger
	// newCA builds the CLM client; overridable in tests to inject a fake.
	newCA func(context.Context) (CA, error)
}

func NewServer(cfg Config, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "certissuer"
	}
	s := &Server{cfg: cfg, log: log.With("plugin", "certissuer")}
	s.newCA = func(context.Context) (CA, error) {
		return NewClient(s.cfg.Addr, s.cfg.Token, s.cfg.Mount), nil
	}
	return s
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	contracts := make([]*pluginv1.ContractDecl, 0, len(facetNamespaces))
	for _, ns := range facetNamespaces {
		contracts = append(contracts, &pluginv1.ContractDecl{SchemaId: ns})
	}
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: "v1",
		// SYNCER class (the Observe registration path checks it); the cert lifecycle
		// is the reconcile ACTUATOR verbs (ADR-0050) — a multi-role Connector.
		Class:            pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:            []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE, pluginv1.Verb_VERB_PLAN, pluginv1.Verb_VERB_APPLY, pluginv1.Verb_VERB_DESTROY},
		Capabilities:     []string{"apply.dry-run"},
		Contracts:        contracts,
		TombstoneSchemes: []string{"cert.serial"},
	}}, nil
}

// Observe performs a full sync: the CLM has no change feed, so each cycle is an
// honest full enumeration streamed as ObservedEntities with the full_sync_complete
// boundary so the host can tombstone (ADR-0042). The CA and revoked certs count as
// absent and are skipped, so a revoke/renew reflects in the graph the same cycle.
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	ctx := stream.Context()
	ca, err := s.newCA(ctx)
	if err != nil {
		return err
	}
	entities, err := observe(ctx, ca, s.log)
	if err != nil {
		return err
	}
	s.log.Info("full sync", "certs", len(entities))
	return stream.Send(&pluginv1.ObserveResponse{Entities: entities, FullSyncComplete: true})
}

// observe enumerates every issued cert and normalizes the live leaf certs. Pure
// content-expertise; no graph writes (the plugin holds no DB path).
func observe(ctx context.Context, ca CA, log *slog.Logger) ([]*pluginv1.ObservedEntity, error) {
	serials, err := ca.ListSerials(ctx)
	if err != nil {
		return nil, err
	}
	var out []*pluginv1.ObservedEntity
	for _, serial := range serials {
		crt, err := ca.GetCert(ctx, serial)
		if err != nil {
			log.Warn("skipping cert (read failed)", "serial", serial, "error", err)
			continue
		}
		if crt.Revoked {
			continue // revoked = absent; the host tombstones any prior Entity
		}
		e, ok, err := normalizeCert(crt)
		if err != nil {
			log.Warn("skipping cert", "serial", serial, "error", err)
			continue
		}
		if !ok {
			continue // CA / non-leaf
		}
		out = append(out, e)
	}
	return out, nil
}

// desired is the reconcile input Contract (actuators/certissuer.input, ADR-0050):
// a valid cert for commonName under role, refreshed before renewBefore. The CSR is
// the TARGET's — the private key is born on the target and never crosses (§2.5).
// The CLM token is NOT here (a spawn-time CredentialRef from the plugin's broker).
type desired struct {
	CommonName  string `json:"commonName"`
	Role        string `json:"role"`
	TTL         string `json:"ttl"`         // lease, e.g. "720h"
	RenewBefore string `json:"renewBefore"` // window, e.g. "168h"
	CSR         string `json:"csr"`         // target-generated CSR PEM (born-on-target)
}

func (d desired) window() (time.Duration, error) {
	if d.RenewBefore == "" {
		return 0, nil
	}
	return time.ParseDuration(d.RenewBefore)
}

// act is the reconcile decision.
type act int

const (
	actNoop act = iota
	actIssue
	actRenew
)

func (a act) String() string {
	switch a {
	case actIssue:
		return "issue"
	case actRenew:
		return "renew"
	default:
		return "noop"
	}
}

// decide is the plugin-owned semantic diff (ADR-0050 §2): no live cert → issue; the
// cert is within renewBefore → renew; else noop (converged). Content-blind to the
// core, which only schedules the opaque converge.
func decide(cur *CurrentCert, renewBefore time.Duration, now time.Time) (act, string) {
	switch {
	case cur == nil:
		return actIssue, "no live cert for commonName — issue"
	case cur.NotAfter.Sub(now) <= renewBefore:
		return actRenew, fmt.Sprintf("cert %s expires %s (within renewBefore) — renew", cur.Serial, cur.NotAfter.UTC().Format(time.RFC3339))
	default:
		return actNoop, fmt.Sprintf("cert %s valid until %s — converged", cur.Serial, cur.NotAfter.UTC().Format(time.RFC3339))
	}
}

// Plan is the reconcile diff (ADR-0050 §2/§4): observe the current cert for the
// commonName and decide issue/renew/noop. The plan is DIAGNOSTIC — certs are
// reconcile-with-desired (Model Y), NOT plan-as-artifact, so no saved_plan is
// produced; Apply RE-DECIDES against live state. summary is the core-legible gist;
// empty == converged.
func (s *Server) Plan(ctx context.Context, req *pluginv1.PlanRequest) (*pluginv1.PlanResponse, error) {
	d, err := parseDesired(req.GetDesired().GetBytes())
	if err != nil {
		return nil, err
	}
	win, err := d.window()
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "certissuer: invalid renewBefore: %v", err)
	}
	ca, err := s.newCA(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := ca.Current(ctx, d.CommonName)
	if err != nil {
		return nil, err
	}
	a, why := decide(cur, win, time.Now())
	diff, _ := json.Marshal(map[string]string{"decision": a.String(), "commonName": d.CommonName}) // redacted — no key material
	return &pluginv1.PlanResponse{
		Diff:    &pluginv1.Payload{Bytes: diff},
		Summary: why,
		Empty:   a == actNoop,
	}, nil
}

// Apply reconciles the commonName to a valid cert: it RE-DECIDES against live state
// (idempotent by re-observation, not a pinned plan), SIGNS the target's CSR via
// OpenBao /sign when issue/renew is needed (born-on-target, §2.5), revokes the
// superseded serial on renew (converge to exactly one valid cert per (CN,role),
// ADR-0050 §5), and writes back the new cert Entity. Terminal ItemResult: CHANGED
// on issue/renew, OK on a converged noop.
func (s *Server) Apply(req *pluginv1.ApplyRequest, stream grpc.ServerStreamingServer[pluginv1.ApplyResponse]) error {
	ctx := stream.Context()
	d, perr := parseDesired(req.GetDesired().GetBytes())
	if perr != nil {
		return applyFail(stream, perr.Error())
	}
	win, err := d.window()
	if err != nil {
		return applyFail(stream, "invalid renewBefore: "+err.Error())
	}
	ca, err := s.newCA(ctx)
	if err != nil {
		return applyFail(stream, err.Error())
	}
	cur, err := ca.Current(ctx, d.CommonName)
	if err != nil {
		return applyFail(stream, err.Error())
	}
	a, why := decide(cur, win, time.Now())
	_ = stream.Send(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, Message: why, At: timestamppb.Now(),
		CorrelationId: req.GetEnvelope().GetCorrelationId(), Fields: map[string]string{"commonName": d.CommonName, "decision": a.String()},
	}})
	if a == actNoop {
		return applyDone(stream, pluginv1.ItemResult_STATUS_OK, "converged", nil)
	}
	// issue/renew both SIGN the target's CSR — never /issue (which would generate +
	// discard a key, shipping a dead cert, ADR-0050 §3). No CSR → fail visibly.
	if d.CSR == "" {
		return applyFail(stream, "commonName requires a target-generated csr (born-on-target, ADR-0050) — refusing to sign without one")
	}
	if req.GetDryRun() {
		return applyDone(stream, pluginv1.ItemResult_STATUS_CHANGED, "dry-run: would "+a.String(), nil)
	}
	iss, err := ca.Sign(ctx, d.Role, d.CSR, d.TTL)
	if err != nil {
		return applyFail(stream, a.String()+": "+err.Error())
	}
	if iss.Serial == "" {
		return applyFail(stream, a.String()+": CLM returned no serial")
	}
	if a == actRenew && cur != nil { // converge to exactly one valid cert per (CN,role)
		if _, err := ca.Revoke(ctx, cur.Serial); err != nil {
			return applyFail(stream, "revoke superseded "+cur.Serial+": "+err.Error())
		}
	}
	// Write back the new cert (identity + label; facets arrive from the Syncer's
	// next poll). The HOST stamps Run provenance + validates (ADR-0047 §2, §7).
	entity := &pluginv1.ObservedEntity{
		Kind: "cert", IdentityKeys: map[string]string{"cert.serial": iss.Serial},
		Labels: map[string]string{"cert.commonName": d.CommonName},
	}
	s.log.Info("signed cert", "serial", iss.Serial, "commonName", d.CommonName, "decision", a.String())
	return applyDone(stream, pluginv1.ItemResult_STATUS_CHANGED, a.String()+" → "+iss.Serial, []*pluginv1.ObservedEntity{entity})
}

// Destroy revokes the cert for the commonName — the gated destructive exception
// (ADR-0050 §6; the Gate is core-side, under one authz/audit, human OR agent). It
// tombstones the cert Entity (GoneEntity by cert.serial) so the graph reflects the
// revoke immediately, symmetric with the Syncer's liveness.
func (s *Server) Destroy(req *pluginv1.DestroyRequest, stream grpc.ServerStreamingServer[pluginv1.DestroyResponse]) error {
	ctx := stream.Context()
	d, perr := parseDesired(req.GetDesired().GetBytes())
	if perr != nil {
		return destroyFail(stream, perr.Error())
	}
	ca, err := s.newCA(ctx)
	if err != nil {
		return destroyFail(stream, err.Error())
	}
	cur, err := ca.Current(ctx, d.CommonName)
	if err != nil {
		return destroyFail(stream, err.Error())
	}
	if cur == nil {
		return stream.Send(&pluginv1.DestroyResponse{
			Event:  &pluginv1.TaskEvent{Terminal: true, Ok: true, At: timestamppb.Now(), Message: "no live cert for " + d.CommonName + " — nothing to revoke"},
			Result: &pluginv1.ItemResult{Status: pluginv1.ItemResult_STATUS_OK},
		})
	}
	if _, err := ca.Revoke(ctx, cur.Serial); err != nil {
		return destroyFail(stream, "revoke "+cur.Serial+": "+err.Error())
	}
	s.log.Info("revoked cert", "serial", cur.Serial, "commonName", d.CommonName)
	return stream.Send(&pluginv1.DestroyResponse{
		Event:  &pluginv1.TaskEvent{Terminal: true, Ok: true, At: timestamppb.Now(), Message: "revoked " + cur.Serial},
		Result: &pluginv1.ItemResult{Status: pluginv1.ItemResult_STATUS_CHANGED},
		Gone:   []*pluginv1.GoneEntity{{Scheme: "cert.serial", Value: cur.Serial}},
	})
}

func parseDesired(raw []byte) (desired, error) {
	var d desired
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, status.Errorf(codes.InvalidArgument, "certissuer: invalid desired: %v", err)
	}
	if d.CommonName == "" || d.Role == "" {
		return d, status.Errorf(codes.InvalidArgument, "certissuer: commonName and role are required")
	}
	return d, nil
}

// applyDone / applyFail / destroyFail emit the single terminal message. A domain
// failure rides the typed descent channel (§1.8), not a transport error.
func applyDone(stream grpc.ServerStreamingServer[pluginv1.ApplyResponse], st pluginv1.ItemResult_Status, msg string, wb []*pluginv1.ObservedEntity) error {
	return stream.Send(&pluginv1.ApplyResponse{
		Event:     &pluginv1.TaskEvent{Terminal: true, Ok: true, At: timestamppb.Now(), Message: msg},
		Result:    &pluginv1.ItemResult{Status: st},
		WriteBack: wb,
	})
}

func applyFail(stream grpc.ServerStreamingServer[pluginv1.ApplyResponse], msg string) error {
	return stream.Send(&pluginv1.ApplyResponse{
		Event:  &pluginv1.TaskEvent{Terminal: true, Ok: false, At: timestamppb.Now(), Level: pluginv1.TaskEvent_LEVEL_ERROR, Message: msg},
		Result: &pluginv1.ItemResult{Status: pluginv1.ItemResult_STATUS_FAILED},
	})
}

func destroyFail(stream grpc.ServerStreamingServer[pluginv1.DestroyResponse], msg string) error {
	return stream.Send(&pluginv1.DestroyResponse{
		Event:  &pluginv1.TaskEvent{Terminal: true, Ok: false, At: timestamppb.Now(), Level: pluginv1.TaskEvent_LEVEL_ERROR, Message: msg},
		Result: &pluginv1.ItemResult{Status: pluginv1.ItemResult_STATUS_FAILED},
	})
}
