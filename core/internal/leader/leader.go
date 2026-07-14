// Package leader provides Kubernetes Lease-based leader election so strattd can
// run as N replicas (HA control plane, ADR-0040). The REST API and the Temporal
// worker run on every replica (stateless / natively multi-worker); the singleton
// controllers — audit sealer, syncers, reconcilers, trigger engine, notifier,
// Salt emitter — run only on the elected leader, with automatic sub-minute
// failover. This is the charter §3 "K8s-native operator posture,
// client-go/controller-runtime" pattern; it adds no dependency (client-go is
// already in the tree).
package leader

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// Config parameterizes the election. Identity must be unique per replica (the
// pod name); Namespace is where the coordination.k8s.io Lease lives.
type Config struct {
	Identity      string
	LeaseName     string
	Namespace     string
	LeaseDuration time.Duration
	RenewDeadline time.Duration
	RetryPeriod   time.Duration
}

func (c Config) withDefaults() Config {
	if c.LeaseName == "" {
		c.LeaseName = "strattd-leader"
	}
	if c.LeaseDuration == 0 {
		c.LeaseDuration = 15 * time.Second
	}
	if c.RenewDeadline == 0 {
		c.RenewDeadline = 10 * time.Second
	}
	if c.RetryPeriod == 0 {
		c.RetryPeriod = 2 * time.Second
	}
	return c
}

// Run contends for leadership until ctx ends. When this replica acquires the
// Lease, onLead is invoked with a leader-scoped context that is cancelled the
// moment leadership is lost — so onLead's controllers stop cleanly on failover.
// After losing leadership (with ctx still alive) it re-contends, so a demoted
// replica becomes a standby that can be re-promoted.
func Run(ctx context.Context, client kubernetes.Interface, cfg Config, log *slog.Logger, onLead func(leaderCtx context.Context)) error {
	cfg = cfg.withDefaults()
	if cfg.Identity == "" {
		return fmt.Errorf("leader: identity required (set POD_NAME)")
	}
	if cfg.Namespace == "" {
		return fmt.Errorf("leader: namespace required (set POD_NAMESPACE)")
	}

	lec := leaderelection.LeaderElectionConfig{
		Lock: &resourcelock.LeaseLock{
			LeaseMeta:  metav1.ObjectMeta{Name: cfg.LeaseName, Namespace: cfg.Namespace},
			Client:     client.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{Identity: cfg.Identity},
		},
		LeaseDuration:   cfg.LeaseDuration,
		RenewDeadline:   cfg.RenewDeadline,
		RetryPeriod:     cfg.RetryPeriod,
		ReleaseOnCancel: true, // fast handoff on graceful shutdown
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leaderCtx context.Context) {
				log.Info("acquired leadership; starting controllers", "identity", cfg.Identity)
				onLead(leaderCtx)
			},
			OnStoppedLeading: func() {
				log.Warn("lost leadership; controllers stopping", "identity", cfg.Identity)
			},
			OnNewLeader: func(id string) {
				if id != cfg.Identity {
					log.Info("observed a different leader", "leader", id)
				}
			},
		},
	}

	// One elector.Run returns when leadership is lost (or ctx ends); loop so a
	// demoted replica keeps standing for re-election.
	for {
		elector, err := leaderelection.NewLeaderElector(lec)
		if err != nil {
			return fmt.Errorf("leader: build elector: %w", err)
		}
		elector.Run(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-time.After(cfg.RetryPeriod):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
