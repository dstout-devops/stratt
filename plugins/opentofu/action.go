package opentofu

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// actionApply is the targetless Action a provisioning build launches (ADR-0145 D1).
//
// WHY AN ACTION AND NOT AN ACTUATION. A Workflow Step is either an actuation
// (viewName + actuator) or a targetless Action, and a `tofu apply` is not an
// actuation in any sense the core means: it converges a WORKSPACE, it reads no
// target set (Apply already folds its result under the workspace-root item_key ""),
// and it has no per-target status to govern. Putting it behind a View would mean
// inventing an anchor View whose members the build ignores — and an anchor View that
// selected nothing would make the build a silent no-op, which is the one outcome a
// provisioning build must never have.
//
// The decisive reason is narrower, though, and it is about the correlation label. A
// build's projection has to carry stratt.intent/singleton or the Finding it was
// launched from never resolves. On the actuation path a plugin's proposed Entities go
// to the graph exactly as the plugin produced them — there is no estate overlay — so
// the label would have to travel out through the module's stratt_entities output. It
// cannot: outputsToWire REFUSES any stratt.*-prefixed label from a module, by design.
// The Action seam is the one that carries an estate overlay (ADR-0058 §6), which is
// why crossplane's subnet builder is one too.
const actionApply = "opentofu/apply"

// applyArgs is the opentofu/apply argument payload (contracts/actions/opentofu/apply.input,
// validated core-side before Invoke). It is a superset of `params`, so prepare() can decode
// the same bytes for the tofu-facing half and ignore the estate overlay.
type applyArgs struct {
	Module        string            `json:"module"`
	Workspace     string            `json:"workspace"`
	Vars          map[string]any    `json:"vars,omitempty"`
	ProjectKind   string            `json:"projectKind"`
	ProjectLabels map[string]string `json:"projectLabels,omitempty"`
}

// Invoke runs opentofu/apply: init + apply the bundled module against the core-injected
// state backend and allocated CIDR, then propose the module's reserved stratt_entities
// output as governed write-back with the estate overlay applied. The plugin holds no
// graph write path (§1.2) — it proposes; the core-side host governs identity, labels and
// Run provenance.
func (s *Server) Invoke(req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	ctx := stream.Context()
	if action := req.GetAction(); action != "" && action != actionApply {
		return status.Errorf(codes.InvalidArgument, "opentofu: unknown action %q", action)
	}
	cid := req.GetEnvelope().GetCorrelationId()

	raw := req.GetArgs().GetBytes()
	var a applyArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return status.Errorf(codes.InvalidArgument, "opentofu/apply: invalid args: %v", err)
		}
	}
	if strings.TrimSpace(a.ProjectKind) == "" {
		// Refused here rather than defaulted. A default would have to be a kind this
		// plugin invented for infrastructure it knows nothing about — the module could
		// be building anything — and the wrong kind lands a real Entity that no View
		// selects and no reconcile closes.
		return s.invokeFailed(stream, cid, fmt.Errorf("opentofu/apply: projectKind is required — the estate names the kind its build projects as, never the provider"))
	}

	// The SAME handles the Apply verb gets (ADR-0105/0145 D2): the s3 backend fills the
	// module's empty `backend "s3" {}` via -backend-config, and the ipam handle's CIDR
	// becomes var.stratt_ipam_cidr. Absent handles are not an error here — the core
	// already failed the Run closed if the declaration required one it could not resolve.
	stateBackend := req.GetResolvedCapabilities()["statestore"]
	ipam := req.GetResolvedCapabilities()["ipam"]
	p, dir, env, varFile, err := s.prepare(raw, stateBackend, ipam)
	if err != nil {
		return s.invokeFailed(stream, cid, fmt.Errorf("opentofu/apply: %w", err))
	}
	if varFile != "" {
		defer os.Remove(varFile)
	}

	var seq int64
	next := func() int64 { return atomic.AddInt64(&seq, 1) }
	onLine := func(line []byte) {
		_ = stream.Send(&pluginv1.InvokeResponse{Event: lineToWire(next(), timestamppb.Now(), line).event})
	}

	if out, rc, ierr := s.run.run(ctx, dir, env, s.initArgs(p.Workspace, stateBackend), onLine); ierr != nil {
		return s.invokeFailed(stream, cid, fmt.Errorf("opentofu/apply: init: %w", ierr))
	} else if rc != 0 {
		emitTail(stream, cid, out)
		return s.invokeFailed(stream, cid, fmt.Errorf("opentofu/apply: tofu init failed (rc=%d)", rc))
	}

	if req.GetDryRun() {
		if _, rc, perr := s.run.run(ctx, dir, env, append([]string{"plan", "-input=false", "-no-color", "-json"}, varFileArg(varFile)...), onLine); perr != nil {
			return s.invokeFailed(stream, cid, fmt.Errorf("opentofu/apply: plan: %w", perr))
		} else if rc != 0 {
			return s.invokeFailed(stream, cid, fmt.Errorf("opentofu/apply: tofu plan failed (rc=%d)", rc))
		}
		// A dry-run projects NOTHING. The plan says what would exist; the graph holds
		// what does (§1.2), and a build Finding that closed on a plan would report
		// infrastructure nobody created.
		return stream.Send(&pluginv1.InvokeResponse{
			Event: &pluginv1.TaskEvent{
				Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(), CorrelationId: cid,
				Terminal: true, Ok: true,
				Message: fmt.Sprintf("dry-run ok: module %s would apply to workspace %s", a.Module, a.Workspace),
			},
			Result: &pluginv1.InvokeResult{OutputContract: contracts.Ref("actions/opentofu/apply.output")},
		})
	}

	applyArgv := append([]string{"apply", "-input=false", "-auto-approve", "-no-color", "-json"}, varFileArg(varFile)...)
	if out, rc, aerr := s.run.run(ctx, dir, env, applyArgv, onLine); aerr != nil {
		return s.invokeFailed(stream, cid, fmt.Errorf("opentofu/apply: %w", aerr))
	} else if rc != 0 {
		// THE CAPTURED OUTPUT IS SURFACED, not discarded. `run` returns everything tofu wrote and
		// this branch used to drop it on the floor, so a failing apply reached the operator as
		// `tofu apply failed (rc=1)` and nothing else — a verdict with no evidence, which is the
		// §1.8 failure DESC-5 had just been fixed for on the Action transport. The per-line stream
		// covers a tofu that TALKS; this covers one that dies having said little, which is exactly
		// the case an operator cannot reconstruct.
		emitTail(stream, cid, out)
		return s.invokeFailed(stream, cid, fmt.Errorf("opentofu/apply: tofu apply failed (rc=%d)", rc))
	}

	outRaw, orc, oerr := s.run.run(ctx, dir, env, []string{"output", "-json", "-no-color"}, nil)
	if oerr != nil || orc != 0 {
		// The apply SUCCEEDED and the read-back failed, so infrastructure exists that
		// nothing has projected. Failing loudly is the only honest outcome: reporting
		// success would close a build Finding with an empty graph behind it, and the
		// next reconcile would offer to build the same thing again (§1.8).
		return s.invokeFailed(stream, cid, fmt.Errorf("opentofu/apply: applied, but reading outputs back failed (rc=%d): %v — infrastructure exists that has not been projected", orc, oerr))
	}
	ents, _, perr := outputsToWire(outRaw)
	if perr != nil {
		return s.invokeFailed(stream, cid, fmt.Errorf("opentofu/apply: %w", perr))
	}
	if err := overlayEntities(ents, a.ProjectKind, a.ProjectLabels); err != nil {
		return s.invokeFailed(stream, cid, fmt.Errorf("opentofu/apply: %w", err))
	}
	if len(ents) == 0 {
		// A module with no stratt_entities builds infrastructure the graph never learns
		// about — the same unprojected-build hole as a failed read-back, arriving as
		// silence instead of an error.
		return s.invokeFailed(stream, cid, fmt.Errorf("opentofu/apply: module %s declares no stratt_entities output — the build would leave nothing in the graph and its Finding would never resolve (ADR-0017)", a.Module))
	}

	outputs, err := actionOutputs(a, outRaw)
	if err != nil {
		return s.invokeFailed(stream, cid, fmt.Errorf("opentofu/apply: %w", err))
	}
	s.log.Info("opentofu build applied", "module", a.Module, "workspace", a.Workspace,
		"projectKind", a.ProjectKind, "entities", len(ents))
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(), CorrelationId: cid,
			Terminal: true, Ok: true,
			Message: fmt.Sprintf("applied %s to workspace %s (%d entities projected)", a.Module, a.Workspace, len(ents)),
			Fields:  map[string]string{"module": a.Module, "workspace": a.Workspace},
		},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: outputs},
			OutputContract: contracts.Ref("actions/opentofu/apply.output"),
			Entities:       ents,
		},
	})
}

// overlayEntities applies the estate's projection overlay to the module's proposed
// Entities (ADR-0058 §6 / ADR-0145 D4).
//
// Conflicts are REFUSED, never merged by precedence. If the module names a kind and the
// estate names a different one, there are two answers to "what is this Entity" and any
// rule for picking between them is the implicit precedence §2.4 exists to forbid —
// silently letting either win lands a real, wrongly-typed Entity, and upsertEntityTx
// retypes on correlate, so the wrong kind propagates rather than sitting still.
func overlayEntities(ents []*pluginv1.ObservedEntity, kind string, labels map[string]string) error {
	for i, e := range ents {
		if k := e.GetKind(); k != "" && k != kind {
			return fmt.Errorf("stratt_entities[%d]: the module projects kind %q but the estate declared projectKind %q — "+
				"one Entity cannot be both, and choosing between them is not the core's call (§2.4). "+
				"Drop the kind from the module's output, or fix the Intent's projectKind", i, k, kind)
		}
		e.Kind = kind
		if len(labels) == 0 {
			continue
		}
		if e.Labels == nil {
			e.Labels = map[string]string{}
		}
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic diagnostics — a map-range error message is a coin flip
		for _, k := range keys {
			if have, ok := e.Labels[k]; ok && have != labels[k] {
				return fmt.Errorf("stratt_entities[%d]: label %q is %q in the module output and %q in the estate overlay — "+
					"refused rather than picked (§2.4)", i, k, have, labels[k])
			}
			e.Labels[k] = labels[k]
		}
	}
	return nil
}

// actionOutputs renders the Action's typed output values: the module's own outputs minus
// the reserved projection channel, with sensitive values masked. The Run captures these for
// cross-Step binding, and a Run's outputs are not a secret channel (§2.5) — the core is
// content-blind and cannot tell which of a module's outputs tofu marked sensitive, so the
// plugin (the content expert) does it here, as redactPlan does for the plan document.
func actionOutputs(a applyArgs, rawOutputs []byte) ([]byte, error) {
	values := map[string]any{}
	if len(rawOutputs) > 0 {
		var outputs map[string]tofuOutput
		if err := json.Unmarshal(rawOutputs, &outputs); err != nil {
			return nil, fmt.Errorf("decode outputs: %w", err)
		}
		for name, o := range outputs {
			if name == "stratt_entities" {
				continue // the governed projection channel, not a value
			}
			if o.Sensitive {
				values[name] = "(sensitive)"
				continue
			}
			var v any
			if err := json.Unmarshal(o.Value, &v); err != nil {
				return nil, fmt.Errorf("decode output %s: %w", name, err)
			}
			values[name] = v
		}
	}
	return json.Marshal(map[string]any{"module": a.Module, "workspace": a.Workspace, "outputs": values})
}

// invokeFailed emits the terminal not-ok TaskEvent — a DOMAIN failure rides the typed
// descent channel (§1.8), never a transport error, so the cause survives to the Run.
func (s *Server) invokeFailed(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], cid string, cause error) error {
	s.log.Error("opentofu action failed", "error", cause)
	return stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_ERROR, At: timestamppb.Now(), CorrelationId: cid,
		Terminal: true, Ok: false, Message: cause.Error(),
	}})
}

// emitTail puts the tail of a failed tofu invocation's captured output onto the Run's event stream.
//
// `run` returns everything the tool wrote and the failure branches used to discard it, so a build
// that died reached the operator as `tofu apply failed (rc=1)` and nothing else. The per-line
// stream already covers a tofu that talks; this covers one that dies having said little — a
// provider that refuses at startup, a backend that rejects a lock, an apply that exits before it
// emits a diagnostic. Those are precisely the failures an operator cannot reconstruct, and
// precisely the ones a rc= verdict describes worst.
//
// Bounded rather than unbounded: the stream is not a log sink (§3 — logs go to Loki), and the last
// lines are where a fatal diagnostic lands. A larger tail would bury the cause it exists to show.
func emitTail(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], cid string, out []byte) {
	const keep = 25
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		_ = stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
			Level: pluginv1.TaskEvent_LEVEL_ERROR, At: timestamppb.Now(), CorrelationId: cid,
			Message: strings.TrimSpace(l), Fields: map[string]string{"kind": "tofu-tail"},
		}})
	}
}
