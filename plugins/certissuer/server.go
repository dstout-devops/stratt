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

// The three Actions this Connector advertises (ActionDecl.name); the
// InvokeRequest.action selector picks one. certissuer is a MULTI-OP Action —
// unlike awsec2's sole create-vm, the empty selector is NOT accepted, every op
// must be named.
const (
	actionIssue  = "certissuer/issue"
	actionRenew  = "certissuer/renew"
	actionRevoke = "certissuer/revoke"
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
// SYNCER-class plugin advertising OBSERVE (the cert Syncer) AND INVOKE (the
// issue/renew/revoke multi-op Action). It advertises the facet namespaces +
// tombstone scheme it REQUESTS to own and the Actions it ships; the core-side host
// honors them only where the operator grant allows. The plugin holds no graph
// write path (§1.2).
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
		PluginId:         s.cfg.PluginID,
		ProtocolVersion:  "v1",
		Class:            pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:            []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE, pluginv1.Verb_VERB_INVOKE},
		Contracts:        contracts,
		TombstoneSchemes: []string{"cert.serial"},
		Actions: []*pluginv1.ActionDecl{
			{
				// issue mints a new leaf; each call is a new cert → not idempotent.
				Name:        actionIssue,
				Input:       &pluginv1.ContractRef{SchemaId: "actions/certissuer/issue.input"},
				Output:      &pluginv1.ContractRef{SchemaId: "actions/certissuer/issue.output"},
				Idempotent:  false,
				DryRunnable: true,
			},
			{
				// renew mints a replacement + revokes the superseded serial → not idempotent.
				Name:        actionRenew,
				Input:       &pluginv1.ContractRef{SchemaId: "actions/certissuer/renew.input"},
				Output:      &pluginv1.ContractRef{SchemaId: "actions/certissuer/renew.output"},
				Idempotent:  false,
				DryRunnable: true,
			},
			{
				// revoke by serial → idempotent (revoking an already-revoked cert is a no-op).
				Name:        actionRevoke,
				Input:       &pluginv1.ContractRef{SchemaId: "actions/certissuer/revoke.input"},
				Output:      &pluginv1.ContractRef{SchemaId: "actions/certissuer/revoke.output"},
				Idempotent:  true,
				DryRunnable: true,
			},
		},
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

// certParams is the input Contract of the three Actions
// (actions/certissuer/<op>.input). The CLM token is NOT here — resolved from the
// plugin's own broker as a spawn-time CredentialRef (§2.5).
type certParams struct {
	Role       string `json:"role"`       // issue/renew: the PKI role to mint under
	CommonName string `json:"commonName"` // issue/renew: the leaf CN
	TTL        string `json:"ttl"`        // issue/renew: lease (e.g. "720h")
	Serial     string `json:"serial"`     // renew: superseded serial to revoke; revoke: target
}

// Invoke runs one cert Action selected by req.Action across issue/renew/revoke: it
// performs the CLM op honoring DryRun and streams a typed progress TaskEvent then a
// TERMINAL InvokeResponse carrying the InvokeResult (typed Outputs, never the key
// or token; for issue also the new cert as an ObservedEntity with Run provenance,
// §1.2). Result is set ONLY on the terminal message.
func (s *Server) Invoke(req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	ctx := stream.Context()
	action := req.GetAction()
	switch action {
	case actionIssue, actionRenew, actionRevoke:
	default:
		return status.Errorf(codes.InvalidArgument, "certissuer: unknown action %q", action)
	}

	var p certParams
	if args := req.GetArgs(); args != nil && len(args.GetBytes()) > 0 {
		if err := json.Unmarshal(args.GetBytes(), &p); err != nil {
			return status.Errorf(codes.InvalidArgument, "%s: invalid args: %v", action, err)
		}
	}
	switch action {
	case actionIssue, actionRenew:
		if p.Role == "" || p.CommonName == "" {
			return status.Errorf(codes.InvalidArgument, "%s requires role and commonName", action)
		}
	case actionRevoke:
		if p.Serial == "" {
			return status.Errorf(codes.InvalidArgument, "certissuer/revoke requires serial")
		}
	}

	// Progress event (typed, core-legible descent — §1.8). Fields never carry the
	// token or a private key.
	if err := stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level:         pluginv1.TaskEvent_LEVEL_INFO,
		Message:       fmt.Sprintf("%s: contacting CLM", action),
		At:            timestamppb.Now(),
		CorrelationId: req.GetEnvelope().GetCorrelationId(),
		Fields:        map[string]string{"commonName": p.CommonName, "serial": p.Serial},
	}}); err != nil {
		return err
	}

	// DryRun is a side-effect-free plan: describe the change, touch no CLM state
	// (§2.2 dry-run). No cert is created/revoked, so the terminal result carries no
	// bindable outputs and no Entity.
	if req.GetDryRun() {
		return stream.Send(&pluginv1.InvokeResponse{
			Event: &pluginv1.TaskEvent{
				Level:         pluginv1.TaskEvent_LEVEL_INFO,
				Message:       fmt.Sprintf("dry-run ok: would %s", action),
				At:            timestamppb.Now(),
				CorrelationId: req.GetEnvelope().GetCorrelationId(),
				Terminal:      true,
				Ok:            true,
			},
			Result: &pluginv1.InvokeResult{OutputContract: outputContract(action)},
		})
	}

	ca, err := s.newCA(ctx)
	if err != nil {
		return err
	}

	switch action {
	case actionIssue:
		return s.invokeIssue(ctx, stream, req, ca, p)
	case actionRenew:
		return s.invokeRenew(ctx, stream, req, ca, p)
	default: // actionRevoke
		return s.invokeRevoke(ctx, stream, req, ca, p)
	}
}

// invokeIssue mints a new leaf cert. Terminal Result carries the issued cert minus
// the private key/token AND the new cert as an ObservedEntity (identity + label);
// its Facets arrive from the certissuer Syncer's next poll (the awsec2 posture).
func (s *Server) invokeIssue(ctx context.Context, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, ca CA, p certParams) error {
	iss, err := ca.Issue(ctx, p.Role, p.CommonName, p.TTL)
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("%s: %w", actionIssue, err))
	}
	if iss.Serial == "" {
		return s.terminalFailure(stream, req, fmt.Errorf("%s: CLM returned no serial", actionIssue))
	}

	out := map[string]any{"serial": iss.Serial, "commonName": p.CommonName}
	if iss.PEM != "" {
		out["certificate"] = iss.PEM // the cert PEM — NOT the private key (§2.5)
	}
	if iss.Expiration > 0 {
		out["notAfter"] = time.Unix(iss.Expiration, 0).UTC().Format(time.RFC3339)
	}
	outputs, err := json.Marshal(out)
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("%s: marshal outputs: %w", actionIssue, err))
	}

	// Project the new cert with Run provenance (§1.2): identity + label only; the
	// Facets come from the Syncer's next enumeration.
	entity := &pluginv1.ObservedEntity{
		Kind:         "cert",
		IdentityKeys: map[string]string{"cert.serial": iss.Serial},
		Labels:       map[string]string{"cert.commonName": p.CommonName},
	}

	s.log.Info("issued cert", "serial", iss.Serial, "commonName", p.CommonName)
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level:         pluginv1.TaskEvent_LEVEL_INFO,
			Message:       "issued " + iss.Serial,
			At:            timestamppb.Now(),
			CorrelationId: req.GetEnvelope().GetCorrelationId(),
			Fields:        map[string]string{"serial": iss.Serial, "commonName": p.CommonName},
			Terminal:      true,
			Ok:            true,
		},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: outputs},
			OutputContract: outputContract(actionIssue),
			Entities:       []*pluginv1.ObservedEntity{entity},
		},
	})
}

// invokeRenew mints a replacement leaf and revokes the superseded serial. Terminal
// Result carries the new + old serials (the certissuer/renew.output shape). The new
// cert's Entity is projected by the Syncer's next poll, not this Action.
func (s *Server) invokeRenew(ctx context.Context, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, ca CA, p certParams) error {
	iss, err := ca.Issue(ctx, p.Role, p.CommonName, p.TTL)
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("%s: %w", actionRenew, err))
	}
	if iss.Serial == "" {
		return s.terminalFailure(stream, req, fmt.Errorf("%s: CLM returned no serial", actionRenew))
	}
	if p.Serial != "" {
		if _, err := ca.Revoke(ctx, p.Serial); err != nil {
			return s.terminalFailure(stream, req, fmt.Errorf("%s: revoke superseded %s: %w", actionRenew, p.Serial, err))
		}
	}

	outputs, err := json.Marshal(map[string]any{"newSerial": iss.Serial, "oldSerial": p.Serial})
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("%s: marshal outputs: %w", actionRenew, err))
	}

	s.log.Info("renewed cert", "newSerial", iss.Serial, "oldSerial", p.Serial)
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level:         pluginv1.TaskEvent_LEVEL_INFO,
			Message:       "renewed → " + iss.Serial,
			At:            timestamppb.Now(),
			CorrelationId: req.GetEnvelope().GetCorrelationId(),
			Fields:        map[string]string{"newSerial": iss.Serial, "oldSerial": p.Serial},
			Terminal:      true,
			Ok:            true,
		},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: outputs},
			OutputContract: outputContract(actionRenew),
		},
	})
}

// invokeRevoke revokes a cert by serial. Terminal Result carries the serial + the
// CLM revocation epoch; it projects no Entity (the Syncer tombstones the revoked
// cert as absent on its next poll).
func (s *Server) invokeRevoke(ctx context.Context, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, ca CA, p certParams) error {
	revocation, err := ca.Revoke(ctx, p.Serial)
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("%s: %w", actionRevoke, err))
	}

	out := map[string]any{"serial": p.Serial}
	if revocation > 0 {
		out["revocationTime"] = revocation
	}
	outputs, err := json.Marshal(out)
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("%s: marshal outputs: %w", actionRevoke, err))
	}

	s.log.Info("revoked cert", "serial", p.Serial)
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level:         pluginv1.TaskEvent_LEVEL_INFO,
			Message:       "revoked " + p.Serial,
			At:            timestamppb.Now(),
			CorrelationId: req.GetEnvelope().GetCorrelationId(),
			Fields:        map[string]string{"serial": p.Serial},
			Terminal:      true,
			Ok:            true,
		},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: outputs},
			OutputContract: outputContract(actionRevoke),
		},
	})
}

// terminalFailure emits the terminal, not-ok TaskEvent (no Result) and returns nil
// — a domain failure rides the typed descent channel, it is not a transport error.
func (s *Server) terminalFailure(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, cause error) error {
	s.log.Error("cert action failed", "error", cause)
	return stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level:         pluginv1.TaskEvent_LEVEL_ERROR,
		Message:       cause.Error(),
		At:            timestamppb.Now(),
		CorrelationId: req.GetEnvelope().GetCorrelationId(),
		Terminal:      true,
		Ok:            false,
	}})
}

// outputContract maps an Action to its pinned output ContractRef.
func outputContract(action string) *pluginv1.ContractRef {
	return &pluginv1.ContractRef{SchemaId: "actions/" + action + ".output"}
}
