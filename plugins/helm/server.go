// Package helm is the Helm Actuator behind the sovereign plugin port (ADR-0092,
// mirroring the OpenTofu Actuator ADR-0016/0047). It runs `helm` as a SUBPROCESS
// (charter §3; §1.5 transports beneath the contract — the helm binary is never
// linked). Plan renders the manifests a human Gate reviews (`helm template`), with
// Secret data redacted (§2.5) and a sha256 the Gate pins; Apply converges the
// release (`helm upgrade --install`, Helm-4 flags), streaming each line as a typed
// TaskEvent (§1.8). No `uninstall`/destroy verb in v1 (ADR-0092 §4). The plugin
// holds no graph write path (§1.2); release-status → Entity projection is a
// follow-up Normalizer.
package helm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

const protocolVersion = "v1"

// actionDeploy is the targetless Action this plugin serves over Invoke: deploy ONE
// release to ONE named namespace (no View anchor). The per-target Actuator (Apply)
// is the fleet-deploy half; the Action is the single-release build (ADR-0092
// dual-surface, mirroring crossplane/provision).
const actionDeploy = "helm/deploy"

// params is the opaque `desired` payload the plugin parses (the core never reads it,
// §1.1). The input Contract (contracts/actuators/helm.input) is validated core-side
// before Execute; `mode` there routes template→Plan / apply→Apply core-side, so it
// is not carried here (mirrors opentofu.input's core-side mode).
type params struct {
	Chart           string         `json:"chart"`
	Repo            string         `json:"repo,omitempty"`
	Version         string         `json:"version,omitempty"`
	Release         string         `json:"release"`
	Namespace       string         `json:"namespace"`
	Values          map[string]any `json:"values,omitempty"`
	CreateNamespace bool           `json:"createNamespace,omitempty"`
}

// Server implements the sovereign plugin port for the Helm Actuator.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg Config
	run commandRunner // injectable — tests drive canned helm output without a helm binary
	log *slog.Logger
}

func NewServer(cfg Config, log *slog.Logger) *Server {
	bin := cfg.HelmBin
	if bin == "" {
		bin = "helm"
	}
	return &Server{cfg: cfg, run: execRunner{bin: bin}, log: log.With("component", "helm-plugin")}
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: protocolVersion,
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_ACTUATOR,
		// PLAN (helm template) + APPLY (helm upgrade --install, per-target Actuator) +
		// INVOKE (the targetless `helm/deploy` Action — deploy one release to one named
		// namespace, no View anchor; ADR-0092 dual-surface). NO DESTROY in v1 (§4) — the
		// uninstall verb arrives with its own review, as opentofu deferred destroy.
		Verbs: []pluginv1.Verb{
			pluginv1.Verb_VERB_PLAN, pluginv1.Verb_VERB_APPLY, pluginv1.Verb_VERB_INVOKE,
		},
		Capabilities: []string{"apply.dry-run"}, // helm upgrade --dry-run=server
		MinProtocol:  protocolVersion,
		MaxProtocol:  protocolVersion,
	}}, nil
}

func (s *Server) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	return &pluginv1.HealthResponse{Status: pluginv1.HealthResponse_SERVING_UP, ProtocolVersion: protocolVersion}, nil
}

// prepare parses params, validates them, renders values to a temp -f file, and
// returns the COMMON helm arg tail (after the subcommand, before verb-specific
// flags): release, chart ref, --namespace, and optional --repo/--version/-f. env
// points helm's caches at a writable home (the pod rootfs is read-only, §7.3), and
// carries the in-cluster kube credentials the plugin inherits.
func (s *Server) prepare(raw []byte) (p params, tail, env []string, valuesFile string, err error) {
	if uerr := json.Unmarshal(raw, &p); uerr != nil {
		return p, nil, nil, "", fmt.Errorf("invalid params: %w", uerr)
	}
	if p.Chart == "" || p.Release == "" || p.Namespace == "" {
		return p, nil, nil, "", fmt.Errorf("chart, release and namespace are required")
	}
	home := s.cfg.HelmHome
	if home == "" {
		home = "/tmp/helm"
	}
	env = append(os.Environ(),
		"HELM_CACHE_HOME="+filepath.Join(home, "cache"),
		"HELM_CONFIG_HOME="+filepath.Join(home, "config"),
		"HELM_DATA_HOME="+filepath.Join(home, "data"),
	)

	tail = []string{p.Release, s.resolveChart(p.Chart), "--namespace", p.Namespace}
	if p.Repo != "" {
		tail = append(tail, "--repo", p.Repo)
	}
	if p.Version != "" {
		tail = append(tail, "--version", p.Version)
	}
	if len(p.Values) > 0 {
		f, ferr := os.CreateTemp(home, "stratt-helm-values-*.json")
		if ferr != nil {
			// Fall back to the default temp dir if HELM_HOME isn't writable yet.
			f, ferr = os.CreateTemp("", "stratt-helm-values-*.json")
		}
		if ferr != nil {
			return p, nil, nil, "", fmt.Errorf("stage values: %w", ferr)
		}
		if eerr := json.NewEncoder(f).Encode(p.Values); eerr != nil {
			_ = f.Close()
			return p, nil, nil, "", fmt.Errorf("encode values: %w", eerr)
		}
		_ = f.Close()
		valuesFile = f.Name()
		tail = append(tail, "-f", valuesFile)
	}
	return p, tail, env, valuesFile, nil
}

// resolveChart maps a chart reference. OCI refs (oci://…) and absolute paths pass
// through; a bare name with ChartRoot set resolves under it (local charts); a repo
// chart (Repo set) passes as the bare name (helm --repo handles the fetch).
func (s *Server) resolveChart(chart string) string {
	if strings.Contains(chart, "://") || strings.HasPrefix(chart, "/") {
		return chart
	}
	if s.cfg.ChartRoot != "" && !strings.Contains(chart, "/") {
		if _, err := os.Stat(filepath.Join(s.cfg.ChartRoot, chart)); err == nil {
			return filepath.Join(s.cfg.ChartRoot, chart)
		}
	}
	return chart
}

// Plan renders the manifests `helm upgrade` WOULD apply (`helm template`), redacts
// Secret data (§2.5), and returns the sha256 a Gate binds. This is the plan-
// equivalent (ADR-0092 §3): read-only, no cluster mutation. v1 apply re-renders
// (the documented drift window, ADR-0016 §6), so the pinned artifact is the reviewed
// template, not a stored plan replayed byte-for-byte.
func (s *Server) Plan(ctx context.Context, req *pluginv1.PlanRequest) (*pluginv1.PlanResponse, error) {
	p, tail, env, valuesFile, err := s.prepare(req.GetDesired().GetBytes())
	if err != nil {
		return nil, err
	}
	if valuesFile != "" {
		defer os.Remove(valuesFile)
	}
	args := append([]string{"template"}, tail...)
	full, rc, rerr := s.run.run(ctx, "", env, args, nil)
	if rerr != nil {
		return nil, fmt.Errorf("helm template: %w", rerr)
	}
	if rc != 0 {
		return nil, fmt.Errorf("helm template failed (rc=%d): %s", rc, tailLines(full, 20))
	}
	redacted := redactManifests(full)
	sum := sha256.Sum256(redacted)
	return &pluginv1.PlanResponse{
		Diff:      &pluginv1.Payload{Bytes: redacted},
		Summary:   fmt.Sprintf("helm template for release %q (chart %q)", p.Release, p.Chart),
		Empty:     false, // v1 has no cheap converged-check; helm diff / SSA dry-run is the follow-up (never claim converged)
		Plan:      &pluginv1.ArtifactRef{Sha256: hex.EncodeToString(sum[:]), MediaType: "application/vnd.helm.manifests+yaml"},
		SavedPlan: redacted, // the CORE re-hashes + content-addresses; redacted so no Secret crosses to the store (§2.5)
	}, nil
}

// Apply converges the release (`helm upgrade --install`, Helm-4 flags), or — with
// dry_run — a server-side dry run that never mutates. It streams each helm line as a
// typed TaskEvent (§1.8) and folds a release-ROOT ItemResult (item_key "" — helm is
// release-scoped, no per-host targets). A pinned plan is not replayed byte-for-byte
// (helm has no saved-plan apply); apply re-renders — the documented drift window.
func (s *Server) Apply(req *pluginv1.ApplyRequest, stream grpc.ServerStreamingServer[pluginv1.ApplyResponse]) error {
	ctx := stream.Context()
	p, tail, env, valuesFile, err := s.prepare(req.GetDesired().GetBytes())
	if err != nil {
		return sendApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, err.Error(), 1)
	}
	if valuesFile != "" {
		defer os.Remove(valuesFile)
	}
	var seq int64
	next := func() int64 { return atomic.AddInt64(&seq, 1) }
	onLine := func(line []byte) {
		if ev := lineToEvent(next(), timestamppb.Now(), line); ev != nil {
			_ = stream.Send(&pluginv1.ApplyResponse{Event: ev})
		}
	}

	_, rc, rerr := s.run.run(ctx, "", env, upgradeArgs(tail, p.CreateNamespace, req.GetDryRun()), onLine)
	if rerr != nil {
		return sendApplyTerminal(stream, false, pluginv1.ItemResult_STATUS_FAILED, rerr.Error(), next())
	}

	// Terminal fold (statuses only escalate — §1.8): rc≠0 → failed; a real apply that
	// succeeds is CHANGED; a successful dry run is OK (validated, nothing mutated).
	status := pluginv1.ItemResult_STATUS_OK
	switch {
	case rc != 0:
		status = pluginv1.ItemResult_STATUS_FAILED
	case !req.GetDryRun():
		status = pluginv1.ItemResult_STATUS_CHANGED
	}
	verb := "upgrade"
	if req.GetDryRun() {
		verb = "dry-run"
	}
	return sendApplyTerminal(stream, rc == 0, status, fmt.Sprintf("helm %s finished rc=%d", verb, rc), next())
}

// sendApplyTerminal emits the single terminal ApplyResponse (event.terminal + the
// release-root ItemResult). ok is advisory — the host folds Succeeded core-side from
// the ItemResult status (ADR-0047 §6).
func sendApplyTerminal(stream grpc.ServerStreamingServer[pluginv1.ApplyResponse], ok bool, status pluginv1.ItemResult_Status, msg string, seq int64) error {
	return stream.Send(&pluginv1.ApplyResponse{
		Event:  &pluginv1.TaskEvent{Terminal: true, Ok: ok, At: timestamppb.Now(), Message: msg, Fields: map[string]string{"kind": "finished"}},
		Result: &pluginv1.ItemResult{ItemKey: "", Status: status},
	})
}

// upgradeArgs builds the `helm upgrade --install` argument list shared by the Apply
// (Actuator) and Invoke (Action) paths. Helm-4 flags (dependency-scout):
// --rollback-on-failure is v4's --atomic; a dry run is server-side and never mutates.
func upgradeArgs(tail []string, createNamespace, dryRun bool) []string {
	args := append([]string{"upgrade", "--install"}, tail...)
	if dryRun {
		return append(args, "--dry-run=server")
	}
	args = append(args, "--rollback-on-failure", "--wait")
	if createNamespace {
		args = append(args, "--create-namespace")
	}
	return args
}

// Invoke serves the targetless `helm/deploy` Action (ADR-0092 dual-surface): deploy
// ONE release to ONE named namespace with no View anchor — the launch path is
// RunAction (targetless), so a self-deploy / single-release build never needs a host
// Entity to point at (mirrors crossplane/provision). It runs the same
// `helm upgrade --install` as Apply, streaming each line as a typed InvokeResponse
// event (§1.8). No graph write-back in v1 (release-status → Entity is a follow-up
// Normalizer). dry_run is a server-side dry run that never mutates.
func (s *Server) Invoke(req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	ctx := stream.Context()
	if action := req.GetAction(); action != "" && action != actionDeploy {
		return status.Errorf(codes.InvalidArgument, "helm: unknown action %q (only %q)", action, actionDeploy)
	}
	cid := req.GetEnvelope().GetCorrelationId()
	p, tail, env, valuesFile, err := s.prepare(req.GetArgs().GetBytes())
	if err != nil {
		return invokeFailed(stream, cid, err)
	}
	if valuesFile != "" {
		defer os.Remove(valuesFile)
	}
	var seq int64
	next := func() int64 { return atomic.AddInt64(&seq, 1) }
	onLine := func(line []byte) {
		if ev := lineToEvent(next(), timestamppb.Now(), line); ev != nil {
			ev.CorrelationId = cid
			_ = stream.Send(&pluginv1.InvokeResponse{Event: ev})
		}
	}
	_, rc, rerr := s.run.run(ctx, "", env, upgradeArgs(tail, p.CreateNamespace, req.GetDryRun()), onLine)
	if rerr != nil {
		return invokeFailed(stream, cid, rerr)
	}
	if rc != 0 {
		return invokeFailed(stream, cid, fmt.Errorf("helm upgrade failed (rc=%d): %s", rc, "see the streamed diagnostics"))
	}
	msg := fmt.Sprintf("helm deployed release %q to namespace %q", p.Release, p.Namespace)
	if req.GetDryRun() {
		msg = fmt.Sprintf("dry-run ok: release %q would deploy to %q", p.Release, p.Namespace)
	}
	// The output Contract (actions/helm/deploy.output) demands the deployed release
	// identity as an object — emit it as the typed Outputs payload, not just the schema
	// ref, or ValidateActionOutputs sees a null and fails the Run.
	outputs, err := json.Marshal(map[string]string{"release": p.Release, "namespace": p.Namespace})
	if err != nil {
		return invokeFailed(stream, cid, err)
	}
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(), CorrelationId: cid,
			Terminal: true, Ok: true, Message: msg, Fields: map[string]string{"kind": "finished", "release": p.Release},
		},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: outputs},
			OutputContract: &pluginv1.ContractRef{SchemaId: "actions/helm/deploy.output"},
		},
	})
}

// invokeFailed emits the terminal not-ok InvokeResponse — a domain failure rides the
// typed descent channel (§1.8), never a bare transport error.
func invokeFailed(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], cid string, cause error) error {
	return stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_ERROR, At: timestamppb.Now(), CorrelationId: cid,
		Terminal: true, Ok: false, Message: cause.Error(), Fields: map[string]string{"kind": "finished"},
	}})
}

// tailLines returns the last n non-empty lines of helm output, for a concise error
// (the full stream already rode the event channel, §1.8).
func tailLines(full []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(full)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
