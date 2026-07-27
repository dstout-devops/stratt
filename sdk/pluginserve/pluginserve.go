// Package pluginserve is the Stratt plugin SDK's scaffolding: the serve loop every plugin
// binary runs, and the port's terminal-frame shapes every plugin has to emit.
//
// WHY THIS EXISTS, and it is not tidiness. The port is the contract (ADR-0046): a plugin is
// conformant because it speaks gRPC + protobuf, not because it imports this package. But the
// FRAMES that contract requires were being reimplemented per plugin, and they had started to
// drift in the way duplicated protocol code always does — `helm` and `opentofu` each carried a
// byte-identical `sendApplyTerminal` whose `seq int64` parameter is read by neither, copied along
// with the function it was already useless in. `crossplane` had the same body a third time.
//
// A wire detail owned by six plugins is six places a fix has to land and five places it will not.
// That is the category this package takes over — not tool logic, which stays where the tool
// knowledge is (§1.4: the spine, and its SDK, learn no tools).
//
// The import direction is the one the architecture endorses: core may depend on sdk, a plugin may
// depend on sdk, a plugin may NEVER depend on core (enforced by `task plugins:boundary`). Nothing
// here reaches toward the control plane.
package pluginserve

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Env reads an environment variable with a default. It was copy-pasted verbatim into eighteen
// plugin entrypoints; one copy is enough.
func Env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// Logger is the stderr text logger every plugin entrypoint was building by hand. A plugin needs
// one BEFORE Main, because NewServer takes it — so it is exported rather than only defaulted
// inside Serve.
func Logger() *slog.Logger { return slog.New(slog.NewTextHandler(os.Stderr, nil)) }

// Config is what a plugin binary supplies to Main beyond its own server.
type Config struct {
	// Server is the plugin's PluginServiceServer implementation — the only genuinely
	// per-plugin thing in a plugin's main().
	Server pluginv1.PluginServiceServer
	// Listen is the bind address. Empty takes STRATT_PLUGIN_LISTEN, then ":9090" — the
	// convention every plugin already used, now stated once instead of twenty times.
	Listen string
	// Log is the logger. Nil builds the stderr text logger every plugin was building.
	Log *slog.Logger
	// Name is the plugin's log identity ("helm", "netbox"), used in the serving line.
	Name string
	// Fields are extra key/value pairs for the serving line — a plugin's own useful context
	// (endpoint, chart root, plugin id). Kept OPEN rather than typed because what is worth
	// logging at start-up is the plugin's business, not this package's.
	Fields []any
}

// Main runs the plugin's gRPC server until the process is stopped, and EXITS non-zero on a
// listen/serve failure.
//
// It exits rather than returning an error deliberately: this is a binary's main(), every caller
// did the same os.Exit(1) dance, and a plugin that cannot listen has nothing to fall back to.
// Callers needing the error (a test, an embedded server) use Serve.
func Main(cfg Config) {
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if err := Serve(cfg, log); err != nil {
		log.Error("plugin serve failed", "plugin", cfg.Name, "error", err)
		os.Exit(1)
	}
}

// Serve is Main with the error returned instead of exited on.
func Serve(cfg Config, log *slog.Logger) error {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	addr := cfg.Listen
	if addr == "" {
		addr = Env("STRATT_PLUGIN_LISTEN", ":9090")
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, cfg.Server)
	log.Info("plugin serving", append([]any{"plugin", cfg.Name, "addr", addr}, cfg.Fields...)...)
	return srv.Serve(lis)
}

// ── terminal frames ──────────────────────────────────────────────────────────
//
// Every streaming verb ends with exactly ONE terminal event, and the core reads that frame to
// decide the item's outcome. Getting its shape wrong is not a cosmetic bug: a stream with no
// terminal frame leaves a Run waiting on a verdict that never arrives.

// ApplyTerminal sends the single terminal frame that ends an Apply/Plan/Destroy stream.
//
// Replaces three byte-identical copies (helm, opentofu, crossplane), two of which carried a
// `seq int64` parameter neither read.
func ApplyTerminal(stream grpc.ServerStreamingServer[pluginv1.ApplyResponse], ok bool, status pluginv1.ItemResult_Status, msg string) error {
	return stream.Send(&pluginv1.ApplyResponse{
		Event:  &pluginv1.TaskEvent{Terminal: true, Ok: ok, At: timestamppb.Now(), Message: msg, Fields: map[string]string{"kind": "finished"}},
		Result: &pluginv1.ItemResult{ItemKey: "", Status: status},
	})
}

// Terminal describes an Invoke stream's closing frame.
type Terminal struct {
	// OutputContract is the pinned schema id of Outputs (ADR-0047 §4). Always set, even on
	// failure: the frame says which contract WOULD have been satisfied.
	OutputContract string
	// OKMessage / FailMessage are the human-facing verdicts. FailMessage empty reuses OKMessage.
	OKMessage   string
	FailMessage string
	// Detail is a failure CLASS, never a driver error string — an error must not embed the
	// endpoint, credential, or payload it failed on (§2.5, §1.8).
	Detail string
	// Outputs is the contract-satisfying payload, sent only on success.
	Outputs []byte
}

// InvokeTerminal sends the single terminal frame that ends an Invoke stream, carrying the
// Action's typed outputs on success.
//
// The correlation id is echoed from the request envelope so the event joins the Run's stream —
// omitting it is a §1.8 descent break that shows up as an orphaned event, not as an error.
func InvokeTerminal(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, ok bool, t Terminal) error {
	ev := &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(), Terminal: true, Ok: ok,
		CorrelationId: req.GetEnvelope().GetCorrelationId(), Message: t.OKMessage,
	}
	if !ok {
		ev.Level = pluginv1.TaskEvent_LEVEL_ERROR
		if t.FailMessage != "" {
			ev.Message = t.FailMessage
		}
		if t.Detail != "" {
			ev.Fields = map[string]string{"detail": t.Detail}
		}
	}
	res := &pluginv1.InvokeResult{OutputContract: &pluginv1.ContractRef{SchemaId: t.OutputContract}}
	if ok && t.Outputs != nil {
		res.Outputs = &pluginv1.Payload{Bytes: t.Outputs}
	}
	return stream.Send(&pluginv1.InvokeResponse{Event: ev, Result: res})
}
