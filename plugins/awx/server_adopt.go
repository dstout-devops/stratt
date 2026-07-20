package awx

import (
	"encoding/json"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/dstout-devops/stratt/plugins/awx/controller"
	"github.com/dstout-devops/stratt/plugins/awx/materialize"
	"github.com/dstout-devops/stratt/sdk/secretbroker"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// The adopt/materialize Action over the sovereign port (ADR-0088/0089). This is the AWX
// breadth that ADR-0089 moved OUT of the control plane: the core resolves the object from the
// graph (tool-blind) and hands the coordinates + AWX CredentialRef COORDINATES here; this
// plugin resolves the token via its own SecretBroker in-pod (§2.5), does the targeted deep-read
// + transform, and returns the reviewable bundle on InvokeResult.Outputs. The token never
// crosses the core.
const (
	actionMaterialize = "adopt/materialize"
	inputContract     = "actions/adopt/materialize.input"
	outputContract    = "actions/adopt/materialize.output"
	// tokenKey is the Secret data key the AWX CredentialRef exposes (the bearer token).
	tokenKey = "token"
)

// materializeArgs is the input Contract (actions/adopt/materialize.input). The AWX token is NOT
// here — it resolves from the CredentialRef named by credentialMount via the SecretBroker.
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
// SecretBroker use-closure (zeroed right after); no endpoint/token/detail leaves on the event
// stream (§2.5).
func (s *Server) Invoke(req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	ctx := stream.Context()
	if action := req.GetAction(); action != "" && action != actionMaterialize {
		return status.Errorf(codes.InvalidArgument, "awx: unknown action %q", action)
	}
	if s.broker == nil {
		return s.adoptTerminal(stream, req, false, "adopt unavailable: this awx plugin has no SecretBroker", nil)
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

	// The AWX CredentialRef — matched by the credentialMount name (the ref the core resolved
	// coordinates for), or the sole credential. Coordinates only; a withheld ref fails closed.
	ref := findCred(req.GetEnvelope().GetCreds(), a.CredentialMount)
	if ref == nil {
		return s.adoptTerminal(stream, req, false, "no adopt credential", nil)
	}
	_ = stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, Message: "materializing adopted object", At: timestamppb.Now(),
		CorrelationId: req.GetEnvelope().GetCorrelationId(),
	}})

	var out *materializeOutput
	var domainErr string
	err := s.broker.WithMaterial(ctx, ref.GetResolved(), func(m secretbroker.Material) error {
		reader := controller.New(controller.Config{Endpoint: a.Endpoint, Token: m.GetString(tokenKey)})
		emit, mErr := materialize.Materialize(ctx, reader, materialize.Args{
			Kind: a.Kind, Identity: a.Identity, NativeID: a.NativeID, Source: a.Source, Live: a.Live,
		})
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
		// A resolution failure (withheld coordinates, missing Secret) is a domain failure on the
		// typed channel (§1.8) — never the token.
		return s.adoptTerminal(stream, req, false, "credential unresolved", nil)
	}
	if out == nil {
		return s.adoptTerminal(stream, req, false, domainErr, nil)
	}
	outputs, mErr := json.Marshal(out)
	if mErr != nil {
		return s.adoptTerminal(stream, req, false, "output encode failed", nil)
	}
	return s.adoptTerminal(stream, req, true, "", outputs)
}

// adoptTerminal sends the single terminal InvokeResponse: ok/detail as the verdict, and (on
// success) the typed bundle + the asserted output-contract id (the core drift-checks it).
func (s *Server) adoptTerminal(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, ok bool, detail string, outputs []byte) error {
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

// findCred matches the AWX credential by the credentialMount name (the CredentialRef name the
// core resolved coordinates for), or returns the sole credential when unset.
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
