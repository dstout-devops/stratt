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
	"github.com/dstout-devops/stratt/core/internal/connectors/vcenter"
	"github.com/dstout-devops/stratt/core/internal/desiredstate"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
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
	dispatcher := dispatch.New(dispatch.Config{
		Namespace: env("STRATT_K8S_NAMESPACE", "default"),
		EEImage:   env("STRATT_EE_IMAGE", "stratt-ee:dev"),
	}, kubeClient, bus, log)

	// In-tree Actuator registry (§2.3); out-of-tree Actuators arrive via the
	// plugin Contract surfaces, not this map.
	registry := map[string]actuators.Actuator{}
	for _, a := range []actuators.Actuator{ansible.Actuator{}, script.Actuator{}} {
		registry[a.Name()] = a
	}

	w := worker.New(temporalClient, orchestrate.TaskQueue, worker.Options{})
	w.RegisterWorkflow(orchestrate.RunAgainstView)
	w.RegisterActivity(&orchestrate.Activities{Store: store, Dispatcher: dispatcher, Bus: bus, Actuators: registry})
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
	} else {
		log.Info("no desired-state checkout configured (STRATT_DESIRED_STATE_PATH empty); reconciliation off")
	}

	// ── interface plane ──────────────────────────────────────────────────
	server := &api.Server{Store: store, Bus: bus, Temporal: temporalClient, Log: log}
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
