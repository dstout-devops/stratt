// Command stratt-agent is the Site satellite (charter §2.3, §3; ADR-0032): a
// remote execution locus's dispatcher. It connects ONLY to its local NATS (a
// leaf of the hub), receives prepared JobSpecs over NATS (push mode) or pulls
// signed Bundles from an OCI registry (pull mode, Commit 2), and runs them
// through the SAME dispatch.Dispatcher the control plane uses — no parallel
// execution stack (§1.4). It resolves credential POINTERS against its OWN local
// Secrets at pod spawn; material never crosses the wire (§2.5). Task events flow
// to its local Bus and leaf-forward to the hub's run-event stream unchanged.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/actuators/script"
	"github.com/dstout-devops/stratt/core/internal/actuators/webhook"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/sitegw"
	"github.com/dstout-devops/stratt/core/internal/siteproto"
	"github.com/dstout-devops/stratt/core/internal/siterelay"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
)

// version stamps liveness; overridden at build time.
var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("stratt-agent exited", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	site := os.Getenv("STRATT_SITE_NAME")
	if site == "" {
		return fmt.Errorf("STRATT_SITE_NAME is required")
	}
	if site == "local" {
		return fmt.Errorf("STRATT_SITE_NAME must not be 'local' (the built-in central locus)")
	}
	mode := env("STRATT_AGENT_MODE", types.SiteModePush)
	url := env("STRATT_NATS_URL", "nats://localhost:4222")

	// Cell binding (ADR-0044 slice 6): this Site belongs to a Cell, and its
	// dispatch/result/event subjects are Cell-scoped. The agent has no database,
	// so it derives the SAME scope token the hub does from shared env
	// (STRATT_CELL_ID, defaulting to LocalCell, plus the STRATT_CELL_DISPATCH_PREFIX
	// override) — set it must match this Site's home Cell, or the agent
	// subscribes to a dispatch subject the hub never publishes to. LocalCell
	// keeps every subject byte-identical to the pre-Cells agent.
	cellID := env("STRATT_CELL_ID", types.LocalCell)
	scopeTok := types.CellScopeToken(cellID, os.Getenv("STRATT_CELL_DISPATCH_PREFIX"))
	if !types.ValidCellScopeToken(scopeTok) {
		return fmt.Errorf("NATS scope token %q (from STRATT_CELL_ID=%q / STRATT_CELL_DISPATCH_PREFIX) is not NATS-safe: use lower-case alphanumeric + '-', no '.'/'*'/'>'", scopeTok, cellID)
	}
	siteproto.SetScope(scopeTok)
	// Log the RESOLVED token, not just the Cell id: the agent has no CaC to
	// reconcile against the hub's, so an operator diagnosing a "dead Site" that
	// is really a hub/agent scope mismatch compares the two ends' natsScope logs
	// directly (charter-guardian slice-6 flag #2).
	log = log.With("site", site, "mode", mode, "cell", cellID, "natsScope", scopeTok)

	// Local event bus: the dispatcher publishes run events here; the leaf
	// forwards the (Cell-scoped) stratt.<cell>.run.> subjects to the hub's
	// stream, so the hub reads them unchanged.
	bus, err := events.Connect(ctx, url, scopeTok)
	if err != nil {
		return fmt.Errorf("events: %w", err)
	}
	defer bus.Close()

	kubeClient, err := kubeClientset()
	if err != nil {
		return fmt.Errorf("kubernetes: %w", err)
	}
	namespace := env("STRATT_K8S_NAMESPACE", "default")
	fsGroup, err := strconv.ParseInt(env("STRATT_EE_FSGROUP", "1000"), 10, 64)
	if err != nil {
		return fmt.Errorf("ee fsgroup: %w", err)
	}
	// The SAME dispatcher the hub uses — stamped with this Site so every event
	// and per-target result records where it ran (§1.8).
	dispatcher := dispatch.New(dispatch.Config{
		Namespace: namespace,
		EEImage:   env("STRATT_EE_IMAGE", "stratt-ee:dev"),
		FSGroup:   fsGroup,
		Site:      site,
	}, kubeClient, bus, log)

	gw, err := sitegw.Connect(url, "stratt-agent/"+site, log)
	if err != nil {
		return fmt.Errorf("sitegw: %w", err)
	}
	defer gw.Close()

	ag := &agent{
		site: site, mode: mode, namespace: namespace,
		dispatcher: dispatcher, gw: gw, kube: kubeClient, bus: bus,
		interp: buildInterpreters(), log: log,
	}

	// Cancellation from the hub → delete this Site's Jobs for that Run.
	unsub, err := gw.SubscribeCancel(site, func(runID string) {
		log.Info("cancel received", "run", runID)
		if err := dispatcher.DeleteRunJobs(context.Background(), runID); err != nil {
			log.Error("cancel cleanup failed", "run", runID, "err", err)
		}
	})
	if err != nil {
		return err
	}
	defer unsub()

	go ag.heartbeatLoop(ctx)

	// Plugin-port relay (ADR-0049): when a Site-local plugin is configured, proxy
	// the hub's relayed port calls to it over the SAME leaf. The agent GOVERNS
	// NOTHING — siterelay.Serve forwards opaque proto; the hub-held grant + gates
	// decide (V1). One plugin per env var today; a plugin manifest is the follow-up.
	if pluginAddr := os.Getenv("STRATT_SITE_PLUGIN_ADDR"); pluginAddr != "" {
		pluginID := env("STRATT_SITE_PLUGIN_ID", "opentofu")
		conn, err := grpc.NewClient(pluginAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("site plugin dial %s: %w", pluginAddr, err)
		}
		defer conn.Close()
		go func() {
			if err := siterelay.Serve(ctx, siterelay.NewNATSAcceptor(gw.Conn(), site, pluginID), pluginv1.NewPluginServiceClient(conn)); err != nil && ctx.Err() == nil {
				log.Error("plugin relay serve exited", "plugin", pluginID, "err", err)
			}
		}()
		log.Info("plugin-port relay serving", "plugin", pluginID, "addr", pluginAddr)
	}

	log.Info("stratt-agent ready", "namespace", namespace, "nats", url, "version", version)
	switch mode {
	case types.SiteModePush:
		return gw.ConsumeDispatch(ctx, site, ag.handlePush)
	case types.SiteModePull:
		return ag.servePull(ctx)
	default:
		return fmt.Errorf("unknown STRATT_AGENT_MODE %q (want push|pull)", mode)
	}
}

// agent holds the Site's execution dependencies.
type agent struct {
	site       string
	mode       string
	namespace  string
	dispatcher *dispatch.Dispatcher
	gw         *sitegw.Gateway
	kube       kubernetes.Interface
	bus        *events.Bus
	interp     map[string]dispatch.Interpreter
	log        *slog.Logger
	lastRun    string // dedup: last Bundle digest executed (pull mode)
}

// handlePush runs one dispatched slice through the shared Dispatcher and reports
// its terminal result. TERMINAL agent-side problems (no interpreter, a missing
// local Secret) publish a result carrying Err and ACK — redelivery cannot fix
// them. A transient infra error from the pod run returns the error to NAK for
// redelivery, while the hub activity keeps awaiting (heartbeated). The result is
// published BEFORE the ACK (in ConsumeDispatch) so an agent crash mid-run
// redelivers the work and adopts the existing Job by name.
func (ag *agent) handlePush(ctx context.Context, req siteproto.DispatchRequest) error {
	if req.Typed {
		return ag.handleTyped(ctx, req)
	}
	name := interpName(req)
	interp, ok := ag.interp[name]
	if !ok {
		ag.log.Error("no interpreter", "name", name, "run", req.RunID)
		return ag.reportTerminal(ctx, req, fmt.Sprintf("site has no interpreter for %q (version skew or unsupported actuator)", name))
	}
	// Preflight: every credential pointer must resolve to a Secret that exists
	// in THIS Site's namespace — material is Site-local, never shipped (§2.5).
	if missing := ag.missingSecrets(ctx, req.Creds); missing != "" {
		return ag.reportTerminal(ctx, req, fmt.Sprintf("credential secret %s not present at site %q", missing, ag.site))
	}
	res, err := ag.dispatcher.Run(ctx, req.RunID, req.Slice, req.Spec, interp, req.Creds, nil)
	if err != nil {
		// Infra error — let the work redeliver (NAK); do not publish a result.
		return fmt.Errorf("dispatch run: %w", err)
	}
	return ag.gw.PublishResult(ctx, siteproto.DispatchResult{
		RunID: req.RunID, Slice: req.Slice, Site: ag.site, Result: *res,
	})
}

// handleTyped runs an EE-Job (subprocess) transport slice at this Site and FORWARDS
// the shim's raw ApplyResponses to the hub (ADR-0051 MF2) — it folds/interprets
// NOTHING (there is no in-agent ansible Interpreter; the hub is the sole governor,
// MF1). §1.8 task events still flow via the dispatcher's local Bus → leaf → hub. A
// preflight failure (missing local Secret) publishes an EOF frame carrying Err and
// ACKs (redelivery cannot fix it); a pod/infra error returns the error to NAK so the
// adopted Job re-runs and re-forwards (frame seqs dedup the replay). On success the
// EOF frame carries the Job exit for the hub's §1.8 fold (MF5).
func (ag *agent) handleTyped(ctx context.Context, req siteproto.DispatchRequest) error {
	if missing := ag.missingSecrets(ctx, req.Creds); missing != "" {
		return ag.reportApplyTerminal(ctx, req, false,
			fmt.Sprintf("credential secret %s not present at site %q", missing, ag.site))
	}
	var seq int64
	forward := func(resp *pluginv1.ApplyResponse) {
		b, err := protojson.Marshal(resp)
		if err != nil {
			// Never silently swallow a frame (§1.8/MF5): log it. A dropped frame
			// fails CLOSED — a lost terminal → the hub's fold reads NOT-OK.
			ag.log.Warn("drop unencodable apply frame", "run", req.RunID, "slice", req.Slice, "err", err)
			return
		}
		seq++
		if perr := ag.gw.PublishApply(ctx, siteproto.ApplyFrame{
			RunID: req.RunID, Slice: req.Slice, Site: ag.site, Seq: seq, Response: b,
		}); perr != nil {
			ag.log.Warn("forward apply frame failed", "run", req.RunID, "slice", req.Slice, "err", perr)
		}
	}
	jobOK, _, err := ag.dispatcher.RunStream(ctx, req.RunID, req.Slice, req.Spec, req.Creds, nil, forward)
	if err != nil {
		// Infra error — NAK for redelivery; publish no EOF (the hub keeps awaiting,
		// heartbeated). The adopted Job re-forwards; seqs dedup the replayed frames.
		return fmt.Errorf("dispatch run (typed): %w", err)
	}
	return ag.reportApplyTerminal(ctx, req, jobOK, "")
}

// reportApplyTerminal publishes the EOF governance frame (the typed path's terminal,
// MF2) and ACKs. A non-empty msg is a terminal agent error the hub surfaces (§1.8).
func (ag *agent) reportApplyTerminal(ctx context.Context, req siteproto.DispatchRequest, jobOK bool, msg string) error {
	if err := ag.gw.PublishApply(ctx, siteproto.ApplyFrame{
		RunID: req.RunID, Slice: req.Slice, Site: ag.site, EOF: true, JobOK: jobOK, Err: msg,
	}); err != nil {
		return err // couldn't report — NAK and retry so the hub eventually learns
	}
	return nil
}

// reportTerminal publishes a terminal-error result and ACKs (returns nil).
func (ag *agent) reportTerminal(ctx context.Context, req siteproto.DispatchRequest, msg string) error {
	if err := ag.gw.PublishResult(ctx, siteproto.DispatchResult{
		RunID: req.RunID, Slice: req.Slice, Site: ag.site, Err: msg,
	}); err != nil {
		return err // couldn't report — NAK and retry so the hub eventually learns
	}
	return nil
}

// missingSecrets returns the name of the first credential Secret absent from
// this Site's namespace, or "" if all are present.
func (ag *agent) missingSecrets(ctx context.Context, creds []dispatch.CredentialMount) string {
	for _, c := range creds {
		ns := c.SecretNamespace
		if ns == "" {
			ns = ag.namespace
		}
		if _, err := ag.kube.CoreV1().Secrets(ns).Get(ctx, c.SecretName, metav1.GetOptions{}); err != nil {
			if k8serrors.IsNotFound(err) {
				return c.SecretName
			}
			// A transient API error is not a missing secret — let the run try.
		}
	}
	return ""
}

// heartbeatLoop TTL-refreshes this agent's liveness in the hub's KV.
func (ag *agent) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	beat := func() {
		l := siteproto.Liveness{Site: ag.site, Mode: ag.mode, Version: version, At: time.Now().UTC().Format(time.RFC3339)}
		if err := ag.gw.Heartbeat(ctx, l); err != nil {
			ag.log.Warn("heartbeat failed", "err", err)
		}
	}
	beat()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			beat()
		}
	}
}

// interpName is the registered Interpreter name for a request (Action wins).
func interpName(req siteproto.DispatchRequest) string {
	if req.Action != "" {
		return req.Action
	}
	if req.Actuator != "" {
		return req.Actuator
	}
	return "ansible"
}

// buildInterpreters is the Site's in-tree Interpreter registry. It MUST track
// strattd's registry (the same binary version in v1) — the hub prepares the
// JobSpec, the agent only Interprets pod output, so config-free constructors
// suffice. Interpreter version skew between hub and agent is the documented
// deepest tension (ADR-0032); v1 ships core in-tree Interpreters only.
//
// Ansible is no longer here (ADR-0051 Phase 5b): it is the EE-Job transport, a
// PluginActuator the hub routes through GovernStream — never the Site fold path.
// A Site-homed ansible Run fails closed at the hub (JobTransportSiteUnsupported)
// until the EE-Job-at-a-Site path lands (Phase 6, MF2). script/webhook still fold.
func buildInterpreters() map[string]dispatch.Interpreter {
	m := map[string]dispatch.Interpreter{}
	for _, a := range []actuators.Actuator{script.Actuator{}, webhook.Actuator{}} {
		m[a.Name()] = a
	}
	// The cert issue/renew/revoke pod Interpreters are retired (ADR-0050): cert
	// lifecycle runs as the certissuer reconcile Actuator over the port, reaching a
	// Site via the plugin relay (ADR-0049), not an in-agent pod Interpreter.
	return m
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// kubeClientset prefers in-cluster config, then KUBECONFIG / ~/.kube/config.
func kubeClientset() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		path := os.Getenv("KUBECONFIG")
		if path == "" {
			home, _ := os.UserHomeDir()
			path = filepath.Join(home, ".kube", "config")
		}
		cfg, err = clientcmd.BuildConfigFromFlags("", path)
		if err != nil {
			return nil, err
		}
	}
	return kubernetes.NewForConfig(cfg)
}
