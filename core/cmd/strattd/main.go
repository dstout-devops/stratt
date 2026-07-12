// Command strattd is the Stratt control-plane server (charter §3): the
// graph-store frontend, the OpenAPI-first API, the Temporal worker for Run
// Workflows, the K8s Job dispatcher, and the Phase-0 vCenter Syncer.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/actuators/ansible"
	"github.com/dstout-devops/stratt/core/internal/actuators/script"
	"github.com/dstout-devops/stratt/core/internal/api"
	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/core/internal/connectors/awsec2"
	"github.com/dstout-devops/stratt/core/internal/connectors/msgraph"
	"github.com/dstout-devops/stratt/core/internal/connectors/vcenter"
	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/desiredstate"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/core/internal/triggers"
	"github.com/dstout-devops/stratt/types"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(ctx, log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("strattd exiting", "error", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func run(ctx context.Context, log *slog.Logger) error {
	// ── graph plane ──────────────────────────────────────────────────────
	store, err := graph.Connect(ctx, env("STRATT_DATABASE_URL", "postgres://stratt:stratt-dev@localhost:5432/stratt"))
	if err != nil {
		return err
	}
	defer store.Close()
	log.Info("graph store ready (migrations applied)")

	// Pin the shipped Contract documents (§1.5, ADR-0015). Drift between a
	// registered pin and the shipped document is blocking — the platform
	// must not boot with schemas that silently changed under their pins.
	shipped, err := contract.All()
	if err != nil {
		return err
	}
	for _, c := range shipped {
		if err := store.RegisterContract(ctx, c); err != nil {
			return err
		}
	}
	log.Info("contracts pinned", "count", len(shipped))

	// Bootstrap ownership registrations (§2.1: registration precedes writes).
	// os.kernel is written back by Runs; owned by the platform team until the
	// Blueprint compiler owns fact routing (Phase 2, charter-guardian note).
	if err := store.RegisterFacetOwner(ctx, types.FacetOwner{
		Namespace: "os.kernel", OwnerKind: "team", OwnerRef: "platform",
	}); err != nil {
		return err
	}

	// ── event plane ──────────────────────────────────────────────────────
	bus, err := events.Connect(ctx, env("STRATT_NATS_URL", "nats://localhost:4222"))
	if err != nil {
		return err
	}
	defer bus.Close()
	log.Info("event bus ready", "stream", events.StreamName)

	// ── orchestration plane ──────────────────────────────────────────────
	temporalClient, err := client.Dial(client.Options{
		HostPort:  env("STRATT_TEMPORAL_ADDRESS", "localhost:7233"),
		Namespace: env("STRATT_TEMPORAL_NAMESPACE", "default"),
		Logger:    tlog{log.With("component", "temporal")},
	})
	if err != nil {
		return fmt.Errorf("temporal: %w", err)
	}
	defer temporalClient.Close()

	// ── actuation plane (K8s Jobs, §3) ───────────────────────────────────
	kubeClient, err := kubeClientset()
	if err != nil {
		return fmt.Errorf("kubernetes: %w", err)
	}
	eeFSGroup, err := strconv.ParseInt(env("STRATT_EE_FSGROUP", "1000"), 10, 64)
	if err != nil {
		return fmt.Errorf("ee fsgroup: %w", err)
	}
	dispatcher := dispatch.New(dispatch.Config{
		Namespace: env("STRATT_K8S_NAMESPACE", "default"),
		EEImage:   env("STRATT_EE_IMAGE", "stratt-ee:dev"),
		FSGroup:   eeFSGroup,
	}, kubeClient, bus, log)

	// ── authorization seam (§2.5, ADR-0009) ─────────────────────────────
	// The CaC tuple evaluator always loads (it is the no-substrate dev path
	// and the model's semantic reference); with STRATT_OPENFGA_URL set the
	// server answers checks instead, fed the same tuples by SyncTuples —
	// two backends, one Authorizer seam, one Git source. Deny is the
	// default: with no tuples loaded, every grant-gated surface refuses.
	evaluator := &authz.TupleAuthorizer{}
	var authorizer authz.Authorizer = evaluator
	var fga *authz.OpenFGAAuthorizer
	if fgaURL := os.Getenv("STRATT_OPENFGA_URL"); fgaURL != "" {
		if fga, err = authz.NewOpenFGAAuthorizer(ctx, fgaURL); err != nil {
			return err
		}
		authorizer = fga
		log.Info("authz backend: openfga", "url", fgaURL)
	} else {
		log.Info("authz backend: in-process tuple evaluator (STRATT_OPENFGA_URL empty)")
	}

	devPrincipal := os.Getenv("STRATT_DEV_PRINCIPAL_HEADER") == "true"
	if devPrincipal {
		log.Warn("DEV PRINCIPAL MODE: X-Stratt-Principal header is trusted — dev harness only (ADR-0009)")
	}
	var oidcResolver *authz.OIDCResolver
	if issuer := os.Getenv("STRATT_OIDC_ISSUER"); issuer != "" {
		// Production guard (ADR-0013, slice-5 guardian flag): an issuer
		// without an audience accepts any token the IdP ever minted for any
		// client. Skipping the audience check is a loud, explicit dev-only
		// opt-out — never a default.
		audience := os.Getenv("STRATT_OIDC_AUDIENCE")
		if audience == "" {
			if os.Getenv("STRATT_OIDC_ALLOW_NO_AUDIENCE") != "true" {
				return fmt.Errorf("STRATT_OIDC_ISSUER is set but STRATT_OIDC_AUDIENCE is empty; set an audience or explicitly set STRATT_OIDC_ALLOW_NO_AUDIENCE=true (dev only)")
			}
			log.Warn("OIDC AUDIENCE CHECK DISABLED (STRATT_OIDC_ALLOW_NO_AUDIENCE=true) — dev harness only")
		}
		// Fail fast: a misconfigured issuer must not boot an API that 401s
		// every Bearer holder while looking healthy.
		if oidcResolver, err = authz.NewOIDCResolver(ctx, issuer, audience); err != nil {
			return err
		}
		log.Info("identity backend: oidc", "issuer", issuer, "audienceCheck", audience != "")
	} else {
		log.Info("identity backend: none (STRATT_OIDC_ISSUER empty); Bearer tokens are not accepted")
	}

	// In-tree Actuator registry (§2.3); out-of-tree Actuators arrive via the
	// plugin Contract surfaces, not this map.
	registry := map[string]actuators.Actuator{}
	for _, a := range []actuators.Actuator{ansible.Actuator{}, script.Actuator{}} {
		registry[a.Name()] = a
	}

	w := worker.New(temporalClient, orchestrate.TaskQueue, worker.Options{})
	w.RegisterWorkflow(orchestrate.RunAgainstView)
	w.RegisterWorkflow(orchestrate.RunDAG)
	w.RegisterActivity(&orchestrate.Activities{Store: store, Dispatcher: dispatcher, Bus: bus, Authz: authorizer, Actuators: registry})
	if err := w.Start(); err != nil {
		return fmt.Errorf("temporal worker: %w", err)
	}
	defer w.Stop()
	log.Info("run worker ready", "taskQueue", orchestrate.TaskQueue)

	// ── Phase-0 vCenter Syncer (started when a Source is configured) ─────
	if endpoint := os.Getenv("STRATT_VCENTER_URL"); endpoint != "" {
		syncer := vcenter.NewSyncer(vcenter.Config{
			Endpoint: endpoint,
			// Credentials via env is the Phase-0 CredentialRef injection
			// stub; material is never persisted (§2.5).
			Username:   env("STRATT_VCENTER_USERNAME", "user"),
			Password:   env("STRATT_VCENTER_PASSWORD", "pass"),
			Insecure:   env("STRATT_VCENTER_INSECURE", "false") == "true",
			SourceName: env("STRATT_VCENTER_SOURCE_NAME", "vcenter-dev"),
		}, store, log)
		if err := syncer.Register(ctx); err != nil {
			return err
		}
		go func() {
			if err := syncer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("vcenter syncer stopped", "error", err)
			}
		}()
	} else {
		log.Info("no vCenter Source configured (STRATT_VCENTER_URL empty); syncer idle")
	}

	// ── MS Graph Syncer (ADR-0014; started when a Source is configured) ──
	if tenant := os.Getenv("STRATT_MSGRAPH_TENANT_ID"); tenant != "" {
		interval, err := time.ParseDuration(env("STRATT_MSGRAPH_INTERVAL", "30s"))
		if err != nil {
			return fmt.Errorf("msgraph interval: %w", err)
		}
		syncer := msgraph.NewSyncer(msgraph.Config{
			Endpoint: env("STRATT_MSGRAPH_ENDPOINT", "https://graph.microsoft.com/v1.0"),
			TenantID: tenant,
			ClientID: os.Getenv("STRATT_MSGRAPH_CLIENT_ID"),
			// Env credential stub, same posture as vCenter (§2.5: material
			// never persists; CredentialRef brokering for Syncers is the
			// recorded follow-up).
			ClientSecret: os.Getenv("STRATT_MSGRAPH_CLIENT_SECRET"),
			TokenURL:     os.Getenv("STRATT_MSGRAPH_TOKEN_URL"),
			SourceName:   env("STRATT_MSGRAPH_SOURCE_NAME", "msgraph"),
		}, interval, store, log)
		if err := syncer.Register(ctx); err != nil {
			return err
		}
		go func() {
			if err := syncer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("msgraph syncer stopped", "error", err)
			}
		}()
	} else {
		log.Info("no MS Graph Source configured (STRATT_MSGRAPH_TENANT_ID empty); syncer idle")
	}

	// ── EC2 cloud-instance Syncer (ADR-0014) ─────────────────────────────
	if region := os.Getenv("STRATT_AWS_REGION"); region != "" {
		interval, err := time.ParseDuration(env("STRATT_AWS_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("awsec2 interval: %w", err)
		}
		syncer := awsec2.NewSyncer(awsec2.Config{
			// Endpoint override points at the moto stand-in in dev;
			// credentials arrive via the SDK's standard env chain (§2.5
			// env-stub posture, CredentialRef brokering is the follow-up).
			Endpoint:   os.Getenv("STRATT_AWS_ENDPOINT"),
			Region:     region,
			SourceName: env("STRATT_AWS_SOURCE_NAME", "awsec2"),
		}, interval, store, log)
		if err := syncer.Register(ctx); err != nil {
			return err
		}
		go func() {
			if err := syncer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("awsec2 syncer stopped", "error", err)
			}
		}()
	} else {
		log.Info("no EC2 Source configured (STRATT_AWS_REGION empty); syncer idle")
	}

	// ── desired-state reconciliation (§1.2: Git is the declarer) ────────
	if path := os.Getenv("STRATT_DESIRED_STATE_PATH"); path != "" {
		interval, err := time.ParseDuration(env("STRATT_DESIRED_STATE_INTERVAL", "30s"))
		if err != nil {
			return fmt.Errorf("desired-state interval: %w", err)
		}
		maxPrune := 0.0 // 0 → controller default (0.5)
		if v := os.Getenv("STRATT_DESIRED_STATE_MAX_PRUNE"); v != "" {
			if maxPrune, err = strconv.ParseFloat(v, 64); err != nil {
				return fmt.Errorf("desired-state max prune: %w", err)
			}
		}
		ctl := &desiredstate.Controller{
			Path: path, Interval: interval, Store: store, Log: log,
			MaxPruneFraction: maxPrune,
		}
		go func() {
			if err := ctl.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("desired-state controller stopped", "error", err)
			}
		}()

		// Authz tuples are CaC in the same checkout (§2.5): load now,
		// reload on the reconcile cadence. A failed reload keeps the
		// previous grant set (never silently drop to deny-all mid-flight;
		// never silently gain grants from a broken file either).
		reloadTuples := func() {
			if err := evaluator.LoadTuples(path); err != nil {
				log.Error("authz tuple reload failed; keeping previous grants", "error", err)
				return
			}
			if fga != nil {
				// OpenFGA is a projection of the same Git source (§1.2):
				// desired-state sync, adds and revokes both.
				if err := fga.SyncTuples(ctx, evaluator.Snapshot()); err != nil {
					log.Error("openfga tuple sync failed; server grants may be stale", "error", err)
				}
			}
		}
		reloadTuples()
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					reloadTuples()
				}
			}
		}()

		// Declared Triggers project onto Temporal Schedules on the same
		// cadence (§3: Temporal owns all lifecycle; ADR-0010) — Git declares,
		// the graph row is the first projection, the Schedule the second.
		trigReconciler := &triggers.Reconciler{
			Temporal: temporalClient, Store: store, Log: log, Interval: interval,
		}
		go func() {
			if err := trigReconciler.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("trigger reconciler stopped", "error", err)
			}
		}()
	} else {
		log.Info("no desired-state checkout configured (STRATT_DESIRED_STATE_PATH empty); reconciliation off — authz has no tuples (deny-all), triggers idle")
	}

	// ── interface plane ──────────────────────────────────────────────────
	uiDir := os.Getenv("STRATT_UI_DIR")
	if uiDir != "" {
		log.Info("serving ui", "dir", uiDir)
	}
	server := &api.Server{Store: store, Bus: bus, Temporal: temporalClient, Authz: authorizer, Log: log, DevPrincipalHeader: devPrincipal, OIDC: oidcResolver, UIDir: uiDir}
	httpSrv := &http.Server{
		Addr:              env("STRATT_LISTEN_ADDR", ":8080"),
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		return ctx.Err()
	}
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

// tlog adapts slog to Temporal's logger interface.
type tlog struct{ l *slog.Logger }

func (t tlog) Debug(msg string, kv ...any) { t.l.Debug(msg, kv...) }
func (t tlog) Info(msg string, kv ...any)  { t.l.Info(msg, kv...) }
func (t tlog) Warn(msg string, kv ...any)  { t.l.Warn(msg, kv...) }
func (t tlog) Error(msg string, kv ...any) { t.l.Error(msg, kv...) }
