package pluginserve

import (
	"fmt"
	"io"
	"os"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// ── the EE-Job transport (ADR-0051) ──────────────────────────────────────────
//
// The sovereign port has TWO transports, and this is the other one. A gRPC plugin is dialed and
// serves a stream; an EE-Job plugin is a ONE-SHOT binary inside an execution image (a K8s Job) —
// the shape charter §3 requires for tools that cannot be linked into the control plane, Ansible
// being the reason it exists.
//
// The two carry the SAME request: `pluginv1.ApplyRequest`, proto-JSON on disk instead of protobuf
// on a wire. The shims already say so in their own doc comments — "the SAME shape the gRPC
// transport sends" — so transport equivalence has always been the design. What was missing was
// SDK support for half of it, which is why all three shims hand-rolled an identical prologue and
// two of them ended up with a BYTE-IDENTICAL `emitFatal`.
//
// This package now covers both halves symmetrically: Main/Serve for gRPC, JobMain/ReadRequest/
// Emitter here. A shim keeps exactly what is its own — mapping the request onto its tool's types
// and running the tool — and nothing that is the port's.

// RequestPath is where the dispatcher stages the ApplyRequest inside the Job.
const RequestPath = "/runner/stratt/request.json"

// ReadRequest loads the staged ApplyRequest. STRATT_REQUEST overrides the path.
//
// Errors are wrapped with the path, because the failure a shim actually hits is a mis-staged
// mount, and "no such file" without the path is a diagnosis the reader has to redo (§1.8).
func ReadRequest() (*pluginv1.ApplyRequest, error) {
	path := Env("STRATT_REQUEST", RequestPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read request %s: %w", path, err)
	}
	var req pluginv1.ApplyRequest
	if err := protojson.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode ApplyRequest from %s: %w", path, err)
	}
	return &req, nil
}

// RunnerDir is the Job's working root. STRATT_RUNNER_DIR overrides; def is the shim's own default
// (`/runner` for ansible-runner's layout, `/runner/project` for a plain content root).
func RunnerDir(def string) string { return Env("STRATT_RUNNER_DIR", def) }

// JobMain is the one-shot entrypoint wrapper: run, and on error print `<name>: err` to stderr and
// exit non-zero.
//
// The exit code is the Job's verdict to Kubernetes, and it is deliberately SEPARATE from the
// terminal frame on stdout: the frame tells the core what happened, the code tells the scheduler.
// A shim that returned an error without exiting non-zero would report a green Job for a failed
// run — which the core would then have to disbelieve.
func JobMain(name string, run func() error) {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, name+":", err)
		os.Exit(1)
	}
}

// Emitter writes the port's typed frames as proto-JSON lines on the Job's stdout — the EE-Job
// transport's equivalent of a gRPC stream Send. The core reads these lines back, governs them
// hub-side, and forwards them as the Run's TaskEvents.
//
// Replaces a closure duplicated in three shim packages and a byte-identical `emitFatal` in two.
type Emitter struct{ w io.Writer }

// NewEmitter writes frames to w (os.Stdout in a real Job; a buffer in a test).
func NewEmitter(w io.Writer) *Emitter { return &Emitter{w: w} }

// Send writes one ApplyResponse frame.
//
// A marshal failure is SWALLOWED, matching what every shim already did, and the reason is worth
// stating rather than inheriting: stdout is the only channel back to the core, so a shim that
// died trying to report has no way to report that it died. Dropping one malformed frame keeps the
// stream alive for the terminal one, which is what the Run's outcome actually hangs on.
func (e *Emitter) Send(r *pluginv1.ApplyResponse) {
	if e == nil || e.w == nil {
		return
	}
	if b, err := protojson.Marshal(r); err == nil {
		_, _ = e.w.Write(b)
		_, _ = e.w.Write([]byte("\n"))
	}
}

// Event writes a non-terminal progress event.
func (e *Emitter) Event(level pluginv1.TaskEvent_Level, msg string, fields map[string]string) {
	e.Send(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Level: level, Message: msg, At: timestamppb.Now(), Fields: fields,
	}})
}

// Fatal writes the terminal failure frame — the shape a shim emits when it cannot even start the
// tool. Always returns nil so a shim can `return e.Fatal(msg)`: the FRAME is the verdict, and
// returning an error here would additionally exit non-zero and double-report the same failure.
func (e *Emitter) Fatal(msg string) error {
	e.Send(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_ERROR, Message: msg, Terminal: true, Ok: false, At: timestamppb.Now(),
	}})
	return nil
}
