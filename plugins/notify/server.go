// Package notify is the notification-delivery Action over the sovereign port
// (ADR-0046/0052). It issues one outbound HTTP POST per Invoke, resolving the Sink's
// per-call url/token via the SDK SecretBroker (the core hands COORDINATES, never
// material — §2.5). The delivery verdict is the Run status (§1.8); it never echoes the
// url, token, or body on the event stream (§2.5 — nothing secret-adjacent leaves).
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/dstout-devops/stratt/sdk/secretbroker"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

const actionWebhook = "notify/webhook"

// Server implements the PluginService for the notify/webhook Action.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	pluginID string
	broker   *secretbroker.Resolver
	client   *http.Client
	log      *slog.Logger
}

// New builds the notify plugin over a SecretBroker resolver.
func New(pluginID string, broker *secretbroker.Resolver, log *slog.Logger) *Server {
	return &Server{
		pluginID: pluginID, broker: broker, log: log,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.pluginID,
		ProtocolVersion: "v1",
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_ACTION,
		Verbs:           []pluginv1.Verb{pluginv1.Verb_VERB_INVOKE},
		Actions: []*pluginv1.ActionDecl{{
			Name:        actionWebhook,
			Input:       &pluginv1.ContractRef{SchemaId: "actions/notify/webhook.input"},
			Output:      &pluginv1.ContractRef{SchemaId: "actions/notify/webhook.output"},
			Idempotent:  false, // a POST is not a no-op; dedup rests on the workflow id
			DryRunnable: false, // a webhook POST has no side-effect-free plan
		}},
	}}, nil
}

// webhookArgs is the input Contract (actions/notify/webhook.input). url/token are NOT
// here — they resolve from the Sink's CredentialRef via the SecretBroker (§2.5).
type webhookArgs struct {
	Body            string            `json:"body"`
	Method          string            `json:"method"`
	Headers         map[string]string `json:"headers"`
	CredentialMount string            `json:"credentialMount"`
}

// Invoke delivers one webhook. It resolves the Sink credential's material INSIDE the
// SecretBroker use-closure (so material is zeroized right after the POST, MF-B), and
// streams a terminal InvokeResponse carrying only the sanitized HTTP verdict.
func (s *Server) Invoke(req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	ctx := stream.Context()
	if action := req.GetAction(); action != "" && action != actionWebhook {
		return status.Errorf(codes.InvalidArgument, "notify: unknown action %q", action)
	}
	var a webhookArgs
	if args := req.GetArgs(); args != nil && len(args.GetBytes()) > 0 {
		if err := json.Unmarshal(args.GetBytes(), &a); err != nil {
			return status.Errorf(codes.InvalidArgument, "notify/webhook: invalid args: %v", err)
		}
	}
	if a.Body == "" {
		return status.Errorf(codes.InvalidArgument, "notify/webhook requires a body")
	}
	switch a.Method {
	case "", "POST":
		a.Method = "POST"
	case "PUT":
	default:
		return status.Errorf(codes.InvalidArgument, "notify/webhook: unsupported method %q (POST, PUT)", a.Method)
	}

	// The Sink's CredentialRef — matched by the credentialMount arg (the ref name
	// RunAction mounted under), or the sole credential. Its coordinates were attached
	// by the core on the local path (MF-C); a withheld ref fails closed in the broker.
	ref := findCred(req.GetEnvelope().GetCreds(), a.CredentialMount)
	if ref == nil {
		return s.terminal(stream, req, false, "no webhook credential")
	}
	_ = stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, Message: "delivering webhook", At: timestamppb.Now(),
		CorrelationId: req.GetEnvelope().GetCorrelationId(),
	}})

	var httpStatus int
	var ok bool
	var detail string
	err := s.broker.WithMaterial(ctx, ref.GetResolved(), func(m secretbroker.Material) error {
		httpStatus, ok, detail = s.post(ctx, m.GetString("url"), m.GetString("token"), a)
		return nil
	})
	if err != nil {
		// A resolution failure (withheld coordinates, missing Secret) is a domain
		// failure on the typed channel (§1.8) — never the url/token.
		return s.terminal(stream, req, false, "credential unresolved")
	}
	_ = httpStatus // the verdict is ok/detail; status stays out of the graph
	return s.terminal(stream, req, ok, detail)
}

// post issues the request and returns a SANITIZED verdict: never the url, token, or
// body, and never a raw error string (urllib/http errors embed the URL — §2.5). Only
// the status class or a failure-class name crosses.
func (s *Server) post(ctx context.Context, url, token string, a webhookArgs) (int, bool, string) {
	httpReq, err := http.NewRequestWithContext(ctx, a.Method, url, strings.NewReader(a.Body))
	if err != nil {
		return 0, false, "request build failed"
	}
	for k, v := range a.Headers {
		httpReq.Header.Set(k, v)
	}
	if httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return 0, false, "delivery failed" // NEVER err.Error() — it embeds the URL
	}
	defer resp.Body.Close()
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	detail := ""
	if !ok {
		detail = fmt.Sprintf("http %d", resp.StatusCode)
	}
	return resp.StatusCode, ok, detail
}

func (s *Server) terminal(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, ok bool, detail string) error {
	ev := &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(), Terminal: true, Ok: ok,
		CorrelationId: req.GetEnvelope().GetCorrelationId(), Message: "webhook delivered",
	}
	if !ok {
		ev.Level = pluginv1.TaskEvent_LEVEL_ERROR
		ev.Message = "webhook delivery failed"
		if detail != "" {
			ev.Fields = map[string]string{"detail": detail}
		}
	}
	return stream.Send(&pluginv1.InvokeResponse{
		Event:  ev,
		Result: &pluginv1.InvokeResult{OutputContract: &pluginv1.ContractRef{SchemaId: "actions/notify/webhook.output"}},
	})
}

// findCred matches the Sink credential by the credentialMount name (the CredentialRef
// name RunAction mounted under), or returns the sole credential when unset.
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
