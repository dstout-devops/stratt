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
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/dstout-devops/stratt/sdk/pluginserve"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// The plugin's OWN contract documents (ADR-0138 D3/D4), embedded so their digests ride
// every ContractRef — the port's invariant #5. The tree was on disk and embedded by
// nothing, so the Manifest carried no ContractRef at all and the pin invariant had no
// subject here; adding the Action made that visible.
//
//go:embed contracts
var contractFS embed.FS

var contracts = pluginserve.Contracts(contractFS)

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
			// INVOKE serves the targetless build Action (ADR-0145 D1). Same plugin, same
			// module root, same core-injected capability handles — a second ENTRY POINT
			// for a workspace-scoped apply, not a second implementation of one.
			pluginv1.Verb_VERB_INVOKE,
		},
		Actions: []*pluginv1.ActionDecl{
			{
				Name:   actionApply,
				Input:  contracts.Ref("actions/opentofu/apply.input"),
				Output: contracts.Ref("actions/opentofu/apply.output"),
				// Idempotent because tofu is: applying an unchanged module against the same
				// workspace state converges to the same infrastructure. That is a property of
				// the STATE BACKEND being the same one, which is why the statestore handle is
				// resolved for this verb too — with local state, a retry after a lost pod
				// would build a second VPC rather than converge on the first.
				Idempotent: true,
				// `tofu plan` is genuinely side-effect-free, so a dry-run Step is honest here.
				DryRunnable: true,
			},
		},
		// apply.dry-run: plan/--check as a streaming dry-run. provisioning (ADR-0112): OpenTofu
		// builds infra a consumer targets — it is a `provisioning` provider (the charter-canonical
		// network builder, §5.1). Advertised unconditionally (running a build module is the plugin's
		// core function); the estate declaration (estate/actuators/opentofu-network.yaml) + its
		// requires:[statestore, ipam] gate any actual build.
		Capabilities: []string{"apply.dry-run", "provisioning"},
		MinProtocol:  protocolVersion,
		MaxProtocol:  protocolVersion,
	}}, nil
}

// dataRoot is where tofu's working state lives — never inside the mounted module tree.
func (s *Server) dataRoot() string {
	if s.cfg.DataRoot != "" {
		return s.cfg.DataRoot
	}
	return filepath.Join(os.TempDir(), "stratt-tofu")
}

func (s *Server) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	return &pluginv1.HealthResponse{Status: pluginv1.HealthResponse_SERVING_UP, ProtocolVersion: protocolVersion}, nil
}

// prepare parses params and builds the tofu run context. varFile is a temp
// -var-file (JSON), "" when no vars. Env carries the per-workspace state
// credential (TF_HTTP_PASSWORD) derived in the plugin, never from the core (§2.5).
func (s *Server) prepare(raw []byte, stateBackend, ipam *pluginv1.CapabilityHandle) (p params, dir string, env []string, varFile string, err error) {
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, "", nil, "", fmt.Errorf("invalid params: %w", err)
	}
	if p.Module == "" || p.Workspace == "" {
		return p, "", nil, "", fmt.Errorf("module and workspace are required")
	}
	// A declared var must never collide with one the CORE injects. The ipam merge below writes
	// stratt_ipam_cidr straight over whatever the declaration set, so an Intent that declared it
	// was silently overruled — two answers to "which range", resolved by a rule nobody wrote
	// down, which is the implicit precedence §2.4 exists to forbid. The reserved prefix is
	// refused instead, and the diagnostic says where the value actually comes from.
	for k := range p.Vars {
		if strings.HasPrefix(k, "stratt_") {
			return p, "", nil, "", fmt.Errorf(
				"var %q uses the reserved stratt_ prefix: these are injected by the core from the "+
					"capability handles the declaration `requires` (ADR-0111/0112 D3), so a declared "+
					"value here would be silently overwritten. Remove it — the allocator decides", k)
		}
	}
	dir = filepath.Join(s.cfg.ModuleRoot, p.Module)
	// PER-WORKSPACE data dir, and OUTSIDE the module tree. It used to be
	// <module>/.terraform — one directory shared by every Run this pod ever serves — which is
	// wrong in two ways that only appear once something actually runs:
	//
	//  1. Two concurrent builds race. .terraform holds the initialized BACKEND (which state key
	//     this directory is bound to), so app-subnet and dmz-subnet building at the same time
	//     would each re-init it under the other, and one would apply against the other's state.
	//     Nothing serializes them; a plugin pod handles Runs concurrently by design.
	//  2. It writes into the mounted MODULE, so the shipped content is no longer what shipped.
	//     That is how `tofu validate` on a checkout started failing after a build ran: the module
	//     directory had acquired a backend binding it does not declare.
	//
	// The provider cache is deliberately still SHARED (TF_PLUGIN_CACHE_DIR): the isolation that
	// matters is per-workspace STATE, and re-downloading a ~600MB provider tree per build would
	// be a large price for isolating something that is identical by construction — the lockfile
	// pins it to one hash.
	dataDir := filepath.Join(s.dataRoot(), "workspaces", p.Workspace)
	cacheDir := filepath.Join(s.dataRoot(), "plugin-cache")
	if err = os.MkdirAll(cacheDir, 0o700); err != nil {
		return p, "", nil, "", fmt.Errorf("prepare tofu plugin cache: %w", err)
	}
	if err = os.MkdirAll(dataDir, 0o700); err != nil {
		return p, "", nil, "", fmt.Errorf("prepare tofu data dir: %w", err)
	}
	env = append(os.Environ(),
		"TF_IN_AUTOMATION=1",
		"TF_DATA_DIR="+dataDir,
		"TF_PLUGIN_CACHE_DIR="+cacheDir,
	)
	// The http-backend FLOOR (ADR-0016) injects a per-workspace HMAC cred. When the core injects a
	// statestore handle (ADR-0105) the backend is provider-resolved instead (e.g. s3, whose creds
	// arrive via the pod's env chain, mounted from the handle's §2.5 CredentialRef) — skip the http
	// cred so we don't send it to a non-http backend.
	if stateBackend == nil && s.cfg.BackendURL != "" {
		env = append(env, "TF_HTTP_USERNAME=stratt", "TF_HTTP_PASSWORD="+s.cfg.workspaceCredential(p.Workspace))
	}
	// Core-injected ipam handle (ADR-0111/0112): merge the NetBox-allocated network identity as
	// module vars so the module references var.stratt_ipam_cidr. The handle's Output carries the
	// contract-validated capabilities/ipam.output payload verbatim (ADR-0112 D2) — a different
	// injection mechanism (a module var) than statestore's -backend-config.
	if ipam != nil && len(ipam.GetOutput()) > 0 {
		var h struct {
			CIDR    string `json:"cidr"`
			VLANID  int    `json:"vlanId"`
			Gateway string `json:"gateway"`
		}
		if uerr := json.Unmarshal(ipam.GetOutput(), &h); uerr == nil && h.CIDR != "" {
			if p.Vars == nil {
				p.Vars = map[string]any{}
			}
			p.Vars["stratt_ipam_cidr"] = h.CIDR
			if h.VLANID != 0 {
				p.Vars["stratt_ipam_vlan_id"] = h.VLANID
			}
			if h.Gateway != "" {
				p.Vars["stratt_ipam_gateway"] = h.Gateway
			}
		}
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
	// -reconfigure, and it is load-bearing rather than defensive. The module directory is a
	// long-lived mount shared by every Run this pod serves, and the backend config is per-WORKSPACE
	// (a different state key each time). tofu detects the change and REFUSES to init, so without
	// this the first build in a pod succeeded and every build after it failed at init — visible
	// only by running two, which nothing ever did.
	//
	// -reconfigure and NOT -migrate-state, which is the same flag family and the opposite meaning:
	// migrating would copy the previous workspace's state onto this workspace's key, so building
	// dmz-subnet would inherit app-subnet's state and then "converge" it by destroying app-subnet's
	// network. Each workspace owns its own state at its own key; there is nothing to migrate.
	if stateBackend != nil || s.cfg.BackendURL != "" {
		args = append(args, "-reconfigure")
	}
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
	ipam := req.GetResolvedCapabilities()["ipam"]               // ADR-0111/0112: nil ⇒ no injected CIDR
	p, dir, env, varFile, err := s.prepare(req.GetDesired().GetBytes(), stateBackend, ipam)
	if err != nil {
		return pluginserve.ApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, err.Error())
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
		return pluginserve.ApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "init: "+ierr.Error())
	} else if rc != 0 {
		return pluginserve.ApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "tofu init failed")
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
				return pluginserve.ApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "pinned plan bytes do not match plan_ref sha256")
			}
		}
		// The pinned plan lands in the WORKSPACE's data dir, not in the module tree — same
		// reason as TF_DATA_DIR above, and sharper here: two concurrent plan-pinned applies
		// writing one path would have each apply the other's approved plan.
		planPath := filepath.Join(s.dataRoot(), "workspaces", p.Workspace, "stratt-pinned.tfplan")
		if werr := os.WriteFile(planPath, req.GetPinnedPlan(), 0o600); werr != nil {
			return pluginserve.ApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, "write pinned plan: "+werr.Error())
		}
		defer os.Remove(planPath)
		args = []string{"apply", "-input=false", "-no-color", "-json", planPath}
	default:
		args = append([]string{"apply", "-input=false", "-auto-approve", "-no-color", "-json"}, varFileArg(varFile)...)
	}
	_, rc, rerr := s.run.run(ctx, dir, env, args, onLine)
	if rerr != nil {
		return pluginserve.ApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, rerr.Error())
	}

	// A successful real apply: lift outputs → write-back + rung-2 DerivedContract.
	if rc == 0 && !req.GetDryRun() {
		outRaw, orc, oerr := s.run.run(ctx, dir, env, []string{"output", "-json", "-no-color"}, nil)
		if oerr == nil && orc == 0 {
			ents, schema, perr := outputsToWire(outRaw)
			if perr != nil {
				// A malformed reserved output fails the Run visibly (§1.8).
				return pluginserve.ApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, perr.Error())
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
	return pluginserve.ApplyTerminal(stream, rc == 0, status, fmt.Sprintf("tofu finished rc=%d", rc))
}

// Plan is the canonical producer of the hash-pinned saved plan (ADR-0047 §7/§8):
// it renders the plan, redacts the human diff (a content-blind core cannot), and
// returns the digest. NOTE (slice 3c open proto point): PlanResponse.plan is an
// ArtifactRef pointer today; delivering the plan BYTES to the core store for
// content-addressed re-hash is the flagged additive proto step. Until then the
// plugin computes the digest but does not yet ship the bytes.
func (s *Server) Plan(ctx context.Context, req *pluginv1.PlanRequest) (*pluginv1.PlanResponse, error) {
	stateBackend := req.GetResolvedCapabilities()["statestore"] // ADR-0105: same handle as Apply
	ipam := req.GetResolvedCapabilities()["ipam"]               // ADR-0111/0112: same handle as Apply
	p, dir, env, varFile, err := s.prepare(req.GetDesired().GetBytes(), stateBackend, ipam)
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
//
// It used to run `tofu destroy` in the process's CWD with no env, no module directory, no
// backend and no vars — reading its own doc comment's claim that "Destroy carries no `desired`",
// which was never true: DestroyRequest.desired has always existed. The consequence was worse than
// a no-op. `tofu destroy` in a directory with no configuration exits ZERO, so the verb reported
// a successful teardown of infrastructure it had not touched, and ADR-0114's decommission Finding
// would have closed on it. A teardown that cannot fail is not a teardown.
//
// It now takes exactly the path Apply does: same params, same module dir, same init against the
// core-injected state backend — because destroying is converging on nothing, and it needs the
// state that says what to converge.
func (s *Server) Destroy(req *pluginv1.DestroyRequest, stream grpc.ServerStreamingServer[pluginv1.DestroyResponse]) error {
	ctx := stream.Context()
	var seq int64
	next := func() int64 { return atomic.AddInt64(&seq, 1) }
	onLine := func(line []byte) {
		_ = stream.Send(&pluginv1.DestroyResponse{Event: lineToWire(next(), timestamppb.Now(), line).event})
	}
	terminal := func(ok bool, msg string) error {
		status := pluginv1.ItemResult_STATUS_OK
		if !ok {
			status = pluginv1.ItemResult_STATUS_FAILED
		}
		return stream.Send(&pluginv1.DestroyResponse{
			Event:  &pluginv1.TaskEvent{Terminal: true, Ok: ok, At: timestamppb.Now(), Message: msg},
			Result: &pluginv1.ItemResult{ItemKey: "", Status: status},
		})
	}

	stateBackend := req.GetResolvedCapabilities()["statestore"]
	// The ipam handle is resolved for DESTROY too, and not as symmetry-for-its-own-sake: a module
	// variable with no default (aws-network's stratt_ipam_cidr) must be set for `tofu destroy` as
	// much as for apply — tofu evaluates the configuration either way. Passing nil here made the
	// teardown fail with "No value for required variable", which reads as a broken module rather
	// than a missing injection.
	ipam := req.GetResolvedCapabilities()["ipam"]
	p, dir, env, varFile, err := s.prepare(req.GetDesired().GetBytes(), stateBackend, ipam)
	if err != nil {
		// Refused rather than defaulted to "destroy whatever is here": guessing the workspace is
		// how a teardown destroys the wrong estate's network.
		return terminal(false, "destroy: "+err.Error())
	}
	if varFile != "" {
		defer os.Remove(varFile)
	}
	if _, rc, ierr := s.run.run(ctx, dir, env, s.initArgs(p.Workspace, stateBackend), onLine); ierr != nil {
		return terminal(false, "destroy init: "+ierr.Error())
	} else if rc != 0 {
		return terminal(false, fmt.Sprintf("destroy: tofu init failed (rc=%d)", rc))
	}
	args := append([]string{"destroy", "-input=false", "-auto-approve", "-no-color", "-json"}, varFileArg(varFile)...)
	_, rc, err := s.run.run(ctx, dir, env, args, onLine)
	ok := err == nil && rc == 0
	return terminal(ok, fmt.Sprintf("tofu destroy rc=%d", rc))
}

func varFileArg(varFile string) []string {
	if varFile == "" {
		return nil
	}
	return []string{"-var-file=" + varFile}
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
