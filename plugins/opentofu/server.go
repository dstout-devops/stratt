// Package opentofu is the OpenTofu Actuator behind the sovereign plugin port
// (ADR-0046/0047, Actuator slice 4). It runs `tofu` as a SUBPROCESS (charter §3)
// and maps its -json stream onto the port: Apply streams typed TaskEvents + drift
// + a workspace-root ItemResult and turns the reserved stratt_entities output into
// governed write-back plus a rung-2 DerivedContract; Plan produces the hash-pinned
// saved plan a Gate approves (ADR-0047 §7/§8). The plugin holds no graph write
// path (§1.2) — it proposes typed values on the wire; the core-side host governs
// what may be written (ownership, identity gating, Run provenance).
package opentofu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

const protocolVersion = "v1"

// params is the opaque `desired` payload the plugin renders (the core never reads
// it, §1.1). The input Contract (contracts/actuators/opentofu.input) is validated
// core-side before Execute; here the content-expert plugin parses it.
type params struct {
	Module    string         `json:"module"`
	Workspace string         `json:"workspace"`
	Vars      map[string]any `json:"vars,omitempty"`
}

// Server implements the sovereign plugin port for the OpenTofu Actuator.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg Config
	run commandRunner // injectable — tests drive canned -json without a tofu binary
	log *slog.Logger
}

func NewServer(cfg Config, log *slog.Logger) *Server {
	bin := cfg.TofuBin
	if bin == "" {
		bin = "tofu"
	}
	return &Server{cfg: cfg, run: execRunner{bin: bin}, log: log.With("component", "opentofu-plugin")}
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: protocolVersion,
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_ACTUATOR,
		Verbs: []pluginv1.Verb{
			pluginv1.Verb_VERB_PLAN, pluginv1.Verb_VERB_APPLY, pluginv1.Verb_VERB_DESTROY,
		},
		Capabilities: []string{"apply.dry-run"}, // plan/--check as a streaming dry-run
		MinProtocol:  protocolVersion,
		MaxProtocol:  protocolVersion,
	}}, nil
}

func (s *Server) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	return &pluginv1.HealthResponse{Status: pluginv1.HealthResponse_SERVING_UP, ProtocolVersion: protocolVersion}, nil
}

// prepare parses params and builds the tofu run context. varFile is a temp
// -var-file (JSON), "" when no vars. Env carries the per-workspace state
// credential (TF_HTTP_PASSWORD) derived in the plugin, never from the core (§2.5).
func (s *Server) prepare(raw []byte, stateBackend *pluginv1.CapabilityHandle) (p params, dir string, env []string, varFile string, err error) {
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, "", nil, "", fmt.Errorf("invalid params: %w", err)
	}
	if p.Module == "" || p.Workspace == "" {
		return p, "", nil, "", fmt.Errorf("module and workspace are required")
	}
	dir = filepath.Join(s.cfg.ModuleRoot, p.Module)
	env = append(os.Environ(),
		"TF_IN_AUTOMATION=1",
		"TF_DATA_DIR="+filepath.Join(dir, ".terraform"),
	)
	// The http-backend FLOOR (ADR-0016) injects a per-workspace HMAC cred. When the core injects a
	// statestore handle (ADR-0105) the backend is provider-resolved instead (e.g. s3, whose creds
	// arrive via the pod's env chain, mounted from the handle's §2.5 CredentialRef) — skip the http
	// cred so we don't send it to a non-http backend.
	if stateBackend == nil && s.cfg.BackendURL != "" {
		env = append(env, "TF_HTTP_USERNAME=stratt", "TF_HTTP_PASSWORD="+s.cfg.workspaceCredential(p.Workspace))
	}
	if len(p.Vars) > 0 {
		f, ferr := os.CreateTemp("", "stratt-tofu-*.tfvars.json")
		if ferr == nil {
			_ = json.NewEncoder(f).Encode(p.Vars)
			_ = f.Close()
			varFile = f.Name()
		}
	}
	return p, dir, env, varFile, nil
}

func (s *Server) initArgs(workspace string, stateBackend *pluginv1.CapabilityHandle) []string {
	args := []string{"init", "-input=false", "-no-color", "-json"}
	if stateBackend != nil {
		// Core-injected statestore backend (ADR-0105): the module declares `backend "<kind>" {}`
		// and the core-resolved settings fill it via -backend-config. Provider-agnostic — s3, gcs,
		// http alike; the consumer just renders the resolved key/values (sorted for determinism).
		cfg := stateBackend.GetConfig()
		keys := make([]string, 0, len(cfg))
		for k := range cfg {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "-backend-config="+k+"="+cfg[k])
		}
		return args
	}
	if s.cfg.BackendURL != "" {
		addr := s.cfg.BackendURL + "/" + workspace
		args = append(args,
			"-backend-config=address="+addr,
			"-backend-config=lock_address="+addr,
			"-backend-config=unlock_address="+addr,
		)
	}
	return args
}

// Apply converges the workspace (or, with dry_run, plans it as a streaming
// diagnostic — never the pin path). It streams each tofu -json line as a
// TaskEvent, lifts drift, and on a successful real apply turns `tofu output -json`
// into governed write-back + the rung-2 DerivedContract. The terminal message
// carries the workspace-ROOT ItemResult (item_key "" — opentofu is workspace-
// scoped, no per-host targets; the host folds it as the root, no confused-deputy).
func (s *Server) Apply(req *pluginv1.ApplyRequest, stream grpc.ServerStreamingServer[pluginv1.ApplyResponse]) error {
	ctx := stream.Context()
	stateBackend := req.GetResolvedCapabilities()["statestore"] // ADR-0105: nil ⇒ the http floor
	p, dir, env, varFile, err := s.prepare(req.GetDesired().GetBytes(), stateBackend)
	if err != nil {
		return sendApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, err.Error(), 1)
	}
	if varFile != "" {
		defer os.Remove(varFile)
	}
	var seq int64
	next := func() int64 { return atomic.AddInt64(&seq, 1) }
	stream0 := func(line []byte) {
		_ = stream.Send(&pluginv1.ApplyResponse{Event: lineToWire(next(), timestamppb.Now(), line).event})
	}

	// tofu init.
	if _, rc, ierr := s.run.run(ctx, dir, env, s.initArgs(p.Workspace, stateBackend), stream0); ierr != nil {
		return sendApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "init: "+ierr.Error(), next())
	} else if rc != 0 {
		return sendApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "tofu init failed", next())
	}

	// tofu apply (or plan, for a streaming dry-run).
	changed := false
	onLine := func(line []byte) {
		w := lineToWire(next(), timestamppb.Now(), line)
		if w.changed {
			changed = true
		}
		resp := &pluginv1.ApplyResponse{Event: w.event}
		if w.drift != nil {
			resp.Drift = w.drift
		}
		_ = stream.Send(resp)
	}
	var args []string
	switch {
	case req.GetDryRun():
		args = append([]string{"plan", "-input=false", "-no-color", "-json"}, varFileArg(varFile)...)
	case len(req.GetPinnedPlan()) > 0:
		// Apply EXACTLY the Gate-approved plan the core verified (ADR-0047 §8): write
		// the pinned bytes and `tofu apply <planfile>` — never re-plan. Defensively
		// re-check the digest the core pinned (belt to the core's verify-don't-trust).
		if ref := req.GetPlanRef().GetSha256(); ref != "" {
			sum := sha256.Sum256(req.GetPinnedPlan())
			if hex.EncodeToString(sum[:]) != ref {
				return sendApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "pinned plan bytes do not match plan_ref sha256", next())
			}
		}
		planPath := filepath.Join(dir, ".terraform", "stratt-pinned.tfplan")
		if werr := os.WriteFile(planPath, req.GetPinnedPlan(), 0o600); werr != nil {
			return sendApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "write pinned plan: "+werr.Error(), next())
		}
		defer os.Remove(planPath)
		args = []string{"apply", "-input=false", "-no-color", "-json", planPath}
	default:
		args = append([]string{"apply", "-input=false", "-auto-approve", "-no-color", "-json"}, varFileArg(varFile)...)
	}
	_, rc, rerr := s.run.run(ctx, dir, env, args, onLine)
	if rerr != nil {
		return sendApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, rerr.Error(), next())
	}

	// A successful real apply: lift outputs → write-back + rung-2 DerivedContract.
	if rc == 0 && !req.GetDryRun() {
		outRaw, orc, oerr := s.run.run(ctx, dir, env, []string{"output", "-json", "-no-color"}, nil)
		if oerr == nil && orc == 0 {
			ents, schema, perr := outputsToWire(outRaw)
			if perr != nil {
				// A malformed reserved output fails the Run visibly (§1.8).
				return sendApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, perr.Error(), next())
			}
			resp := &pluginv1.ApplyResponse{
				Event:     &pluginv1.TaskEvent{Level: pluginv1.TaskEvent_LEVEL_INFO, Message: "outputs", At: timestamppb.Now(), Fields: map[string]string{"kind": "outputs"}},
				WriteBack: ents,
			}
			if len(schema) > 0 {
				resp.DerivedContract = &pluginv1.DerivedContract{
					Rung:     pluginv1.DerivedContract_RUNG_TOOL_DERIVED,
					SchemaId: "opentofu/" + p.Workspace + ".outputs",
					Rev:      "tool-derived",
					Schema:   schema,
				}
			}
			_ = stream.Send(resp)
		}
	}

	// Terminal fold. rc!=0 → failed; a successful apply is "changed"; a dry-run
	// plan is "changed" only if it would change (statuses only escalate, §1.8).
	status := pluginv1.ItemResult_STATUS_OK
	switch {
	case rc != 0:
		status = pluginv1.ItemResult_STATUS_FAILED
	case !req.GetDryRun():
		status = pluginv1.ItemResult_STATUS_CHANGED
	case changed:
		status = pluginv1.ItemResult_STATUS_CHANGED
	}
	return sendApplyTerminal(stream, rc == 0, status, fmt.Sprintf("tofu finished rc=%d", rc), next())
}

// Plan is the canonical producer of the hash-pinned saved plan (ADR-0047 §7/§8):
// it renders the plan, redacts the human diff (a content-blind core cannot), and
// returns the digest. NOTE (slice 3c open proto point): PlanResponse.plan is an
// ArtifactRef pointer today; delivering the plan BYTES to the core store for
// content-addressed re-hash is the flagged additive proto step. Until then the
// plugin computes the digest but does not yet ship the bytes.
func (s *Server) Plan(ctx context.Context, req *pluginv1.PlanRequest) (*pluginv1.PlanResponse, error) {
	stateBackend := req.GetResolvedCapabilities()["statestore"] // ADR-0105: same handle as Apply
	p, dir, env, varFile, err := s.prepare(req.GetDesired().GetBytes(), stateBackend)
	if err != nil {
		return nil, err
	}
	if varFile != "" {
		defer os.Remove(varFile)
	}
	if _, rc, ierr := s.run.run(ctx, dir, env, s.initArgs(p.Workspace, stateBackend), nil); ierr != nil || rc != 0 {
		return nil, fmt.Errorf("tofu init failed (rc=%d): %v", rc, ierr)
	}
	planPath := filepath.Join(dir, ".terraform", "stratt.tfplan")
	planArgs := append([]string{"plan", "-input=false", "-no-color", "-json", "-out=" + planPath}, varFileArg(varFile)...)
	if _, rc, perr := s.run.run(ctx, dir, env, planArgs, nil); perr != nil || rc > 1 {
		return nil, fmt.Errorf("tofu plan failed (rc=%d): %v", rc, perr)
	}
	// The saved plan file is the pinnable artifact; its sha256 is the digest a Gate
	// binds. `tofu show -json` gives the human/descent diff, redacted (§2.5).
	planBytes, rerr := os.ReadFile(planPath)
	if rerr != nil {
		return nil, fmt.Errorf("read saved plan: %w", rerr)
	}
	sum := sha256.Sum256(planBytes)
	digest := hex.EncodeToString(sum[:])
	showRaw, _, _ := s.run.run(ctx, dir, env, []string{"show", "-json", "-no-color", planPath}, nil)
	redacted := redactPlan(showRaw)
	empty := !planHasChanges(showRaw)
	return &pluginv1.PlanResponse{
		Diff:      &pluginv1.Payload{Bytes: redacted},
		Summary:   fmt.Sprintf("opentofu plan for workspace %q", p.Workspace),
		Empty:     empty,
		Plan:      &pluginv1.ArtifactRef{Sha256: digest, MediaType: "application/vnd.opentofu.plan"},
		SavedPlan: planBytes, // opaque; the CORE re-hashes + content-addresses (§8)
	}, nil
}

// Destroy tears the workspace down; streams like Apply with a workspace-root status.
func (s *Server) Destroy(req *pluginv1.DestroyRequest, stream grpc.ServerStreamingServer[pluginv1.DestroyResponse]) error {
	ctx := stream.Context()
	// Destroy carries no `desired` — the workspace identity rides the same params
	// contract via a dedicated field is a follow-up; v1 destroys the initialized
	// workspace in the module dir. (Kept minimal; the Apply path is the proof.)
	var seq int64
	next := func() int64 { return atomic.AddInt64(&seq, 1) }
	onLine := func(line []byte) {
		_ = stream.Send(&pluginv1.DestroyResponse{Event: lineToWire(next(), timestamppb.Now(), line).event})
	}
	_, rc, err := s.run.run(ctx, "", nil, []string{"destroy", "-input=false", "-auto-approve", "-no-color", "-json"}, onLine)
	ok := err == nil && rc == 0
	status := pluginv1.ItemResult_STATUS_OK
	if !ok {
		status = pluginv1.ItemResult_STATUS_FAILED
	}
	return stream.Send(&pluginv1.DestroyResponse{
		Event:  &pluginv1.TaskEvent{Terminal: true, Ok: ok, At: timestamppb.Now(), Message: fmt.Sprintf("tofu destroy rc=%d", rc)},
		Result: &pluginv1.ItemResult{ItemKey: "", Status: status},
	})
}

func varFileArg(varFile string) []string {
	if varFile == "" {
		return nil
	}
	return []string{"-var-file=" + varFile}
}

// sendApplyTerminal emits the single terminal ApplyResponse (event.terminal + the
// workspace-root ItemResult). ok is advisory — the host folds Succeeded core-side
// from the ItemResult status (ADR-0047 §6, guardian fix #3).
func sendApplyTerminal(stream grpc.ServerStreamingServer[pluginv1.ApplyResponse], ok bool, status pluginv1.ItemResult_Status, msg string, seq int64) error {
	return stream.Send(&pluginv1.ApplyResponse{
		Event:  &pluginv1.TaskEvent{Terminal: true, Ok: ok, At: timestamppb.Now(), Message: msg, Fields: map[string]string{"kind": "finished"}},
		Result: &pluginv1.ItemResult{ItemKey: "", Status: status},
	})
}

// planHasChanges reports whether a `tofu show -json` plan has any resource change
// other than no-op — used to set PlanResponse.empty (converged).
func planHasChanges(showRaw []byte) bool {
	var doc struct {
		ResourceChanges []struct {
			Change struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(showRaw, &doc); err != nil {
		return true // unknown → assume change (never claim converged on bad data)
	}
	for _, rc := range doc.ResourceChanges {
		for _, a := range rc.Change.Actions {
			if a != "no-op" && a != "read" {
				return true
			}
		}
	}
	return false
}
