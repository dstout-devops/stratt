// Package adoptplugin serves the adopt/materialize Action over the sovereign plugin port
// (ADR-0088). It is a CORE-OWNED Action, not a dark-matter plugin: its Invoke runs the core
// awximport transform (core/internal/awximport), which is why the server lives in the core
// module and ships as a core-owned image rather than an SDK-only plugin. The control plane
// hands the core-RESOLVED coordinates (kind/identity/endpoint/nativeId/source/live) as the
// Action args and the AWX CredentialRef as Envelope COORDINATES (never material, §2.5); this
// process resolves the token via the SDK SecretBroker under its OWN confined RBAC, inside a
// use-closure zeroed on return, does the targeted read-only deep-read + transform, and returns
// the reviewable bundle on InvokeResult.Outputs. The AWX token never crosses the core.
package adoptplugin

import (
	"context"
	"encoding/json"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/dstout-devops/stratt/core/internal/adopt"
	"github.com/dstout-devops/stratt/core/internal/awximport/awx"
	"github.com/dstout-devops/stratt/sdk/secretbroker"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

const (
	actionMaterialize = "adopt/materialize"
	inputContract     = "actions/adopt/materialize.input"
	outputContract    = "actions/adopt/materialize.output"
	// tokenKey is the Secret data key the AWX CredentialRef exposes (the bearer token).
	tokenKey = "token"
)

// Server implements the PluginService for the adopt/materialize Action.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	pluginID string
	broker   *secretbroker.Resolver
	// newReader builds the targeted AWX deep-reader; swappable in tests (a fake AWX).
	newReader func(endpoint, token string) adopt.Reader
	log       *slog.Logger
}

// New builds the adopt plugin over a SecretBroker resolver.
func New(pluginID string, broker *secretbroker.Resolver, log *slog.Logger) *Server {
	return &Server{
		pluginID: pluginID, broker: broker, log: log,
		newReader: func(endpoint, token string) adopt.Reader {
			return awx.New(awx.Config{Endpoint: endpoint, Token: token})
		},
	}
}

// GetManifest declares the adopt/materialize Action + its input/output Contracts (Class
// ACTION / Verb INVOKE), exactly like the notify plugin (ADR-0088 Decision §2).
func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.pluginID,
		ProtocolVersion: "v1",
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_ACTION,
		Verbs:           []pluginv1.Verb{pluginv1.Verb_VERB_INVOKE},
		Actions: []*pluginv1.ActionDecl{{
			Name:        actionMaterialize,
			Input:       &pluginv1.ContractRef{SchemaId: inputContract},
			Output:      &pluginv1.ContractRef{SchemaId: outputContract},
			Idempotent:  true,  // a read + transform has no side effect; re-runs re-emit the same bundle
			DryRunnable: false, // adopt is already a read-only proposal; there is no separate plan
		}},
	}}, nil
}

// materializeArgs is the input Contract (actions/adopt/materialize.input). The AWX token is
// NOT here — it resolves from the CredentialRef named by credentialMount via the SecretBroker.
type materializeArgs struct {
	Kind            string   `json:"kind"`
	Identity        string   `json:"identity"`
	Endpoint        string   `json:"endpoint"`
	NativeID        int      `json:"nativeId"`
	Source          string   `json:"source"`
	Live            []string `json:"live"`
	CredentialMount string   `json:"credentialMount"`
}

// materializeOutput is the output Contract (actions/adopt/materialize.output).
type materializeOutput struct {
	Files  map[string]string `json:"files"`
	Report string            `json:"report"`
}

// Invoke resolves the AWX token in-pod, does the targeted deep-read + transform, and returns
// the reviewable bundle on InvokeResult.Outputs. Material is dereferenced only inside the
// SecretBroker use-closure (zeroed right after, the notify MF-B pattern); no url/token/detail
// that could carry the endpoint leaves on the event stream (§2.5).
func (s *Server) Invoke(req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	ctx := stream.Context()
	if action := req.GetAction(); action != "" && action != actionMaterialize {
		return status.Errorf(codes.InvalidArgument, "adopt: unknown action %q", action)
	}
	var a materializeArgs
	if args := req.GetArgs(); args != nil && len(args.GetBytes()) > 0 {
		if err := json.Unmarshal(args.GetBytes(), &a); err != nil {
			return status.Errorf(codes.InvalidArgument, "adopt/materialize: invalid args: %v", err)
		}
	}
	if a.Kind == "" || a.Identity == "" || a.Endpoint == "" {
		return status.Errorf(codes.InvalidArgument, "adopt/materialize requires kind, identity, endpoint")
	}

	// The AWX CredentialRef — matched by the credentialMount name (the ref RunAction resolved
	// coordinates for), or the sole credential. Coordinates only; a withheld ref fails closed.
	ref := findCred(req.GetEnvelope().GetCreds(), a.CredentialMount)
	if ref == nil {
		return s.terminal(stream, req, false, "no adopt credential", nil)
	}
	_ = stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, Message: "materializing adopted object", At: timestamppb.Now(),
		CorrelationId: req.GetEnvelope().GetCorrelationId(),
	}})

	var out *materializeOutput
	var domainErr string
	err := s.broker.WithMaterial(ctx, ref.GetResolved(), func(m secretbroker.Material) error {
		reader := s.newReader(a.Endpoint, m.GetString(tokenKey))
		emit, mErr := adopt.Materialize(ctx, reader,
			adopt.Request{Kind: a.Kind, Identity: a.Identity},
			adopt.Resolved{NativeID: a.NativeID, Source: a.Source, Live: a.Live})
		if mErr != nil {
			// Sanitized: never echo the endpoint/token or a raw transport error (§2.5/§1.8).
			s.log.Error("adopt materialize failed", "kind", a.Kind, "identity", a.Identity, "error", mErr)
			domainErr = "targeted read/transform failed"
			return nil
		}
		out = &materializeOutput{Files: emit.Files, Report: emit.Report}
		return nil
	})
	if err != nil {
		// A resolution failure (withheld coordinates, missing Secret) is a domain failure on
		// the typed channel (§1.8) — never the token.
		return s.terminal(stream, req, false, "credential unresolved", nil)
	}
	if out == nil {
		return s.terminal(stream, req, false, domainErr, nil)
	}
	outputs, mErr := json.Marshal(out)
	if mErr != nil {
		return s.terminal(stream, req, false, "output encode failed", nil)
	}
	return s.terminal(stream, req, true, "", outputs)
}

// terminal sends the single terminal InvokeResponse: ok/detail as the verdict, and (on
// success) the typed bundle + the asserted output-contract id (the core drift-checks it).
func (s *Server) terminal(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, ok bool, detail string, outputs []byte) error {
	ev := &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(), Terminal: true, Ok: ok,
		CorrelationId: req.GetEnvelope().GetCorrelationId(), Message: "adopt materialized",
	}
	if !ok {
		ev.Level = pluginv1.TaskEvent_LEVEL_ERROR
		ev.Message = "adopt materialize failed"
		if detail != "" {
			ev.Fields = map[string]string{"detail": detail}
		}
	}
	res := &pluginv1.InvokeResult{OutputContract: &pluginv1.ContractRef{SchemaId: outputContract}}
	if ok && outputs != nil {
		res.Outputs = &pluginv1.Payload{Bytes: outputs}
	}
	return stream.Send(&pluginv1.InvokeResponse{Event: ev, Result: res})
}

// findCred matches the AWX credential by the credentialMount name (the CredentialRef name
// the core resolved coordinates for), or returns the sole credential when unset.
func findCred(creds []*pluginv1.CredentialRef, mount string) *pluginv1.CredentialRef {
	if mount != "" {
		for _, c := range creds {
			if c.GetName() == mount {
				return c
			}
		}
		return nil
	}
	if len(creds) == 1 {
		return creds[0]
	}
	return nil
}
