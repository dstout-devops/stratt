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

	certaction "github.com/dstout-devops/stratt/core/internal/actions/certissuer"
	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/actuators/ansible"
	"github.com/dstout-devops/stratt/core/internal/actuators/script"
	"github.com/dstout-devops/stratt/core/internal/actuators/webhook"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/sitegw"
	"github.com/dstout-devops/stratt/core/internal/siteproto"
	"github.com/dstout-devops/stratt/types"
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
	log = log.With("site", site, "mode", mode)

	// Local event bus: the dispatcher publishes run events here; the leaf
	// forwards stratt.run.> to the hub's stream, so the hub reads them unchanged.
	bus, err := events.Connect(ctx, url)
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
func buildInterpreters() map[string]dispatch.Interpreter {
	m := map[string]dispatch.Interpreter{}
	for _, a := range []actuators.Actuator{ansible.Actuator{}, script.Actuator{}, webhook.Actuator{}} {
		m[a.Name()] = a
	}
	// Action Interpreters (Prepare is hub-side; Interpret is config-free here).
	for _, act := range []dispatch.Interpreter{certaction.Issue(), certaction.Renew(), certaction.Revoke()} {
		if n, ok := act.(interface{ Name() string }); ok {
			m[n.Name()] = act
		}
	}
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
