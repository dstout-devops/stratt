package desiredstate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"log/slog"

	"github.com/dstout-devops/stratt/core/internal/graph"
)

// Controller is the desired-state reconcile loop (charter §3 reconciliation
// controllers): every interval it refreshes the declarations checkout and
// applies it. Fail-safe by construction — a pull or parse failure skips the
// cycle; pruning never runs off a broken read.
type Controller struct {
	// Path is the declarations checkout (contains views/). If it is a git
	// checkout (.git present), the loop fast-forwards it before reconciling;
	// a plain directory is reconciled as-is.
	Path     string
	Interval time.Duration
	Store    *graph.Store
	Log      *slog.Logger
	// MaxPruneFraction is the unattended blast-radius guard (§4.3 spirit,
	// mandatory gate lands with the Phase-2 max-delta machinery): a cycle
	// whose plan would prune MORE than this fraction of the current
	// cac-declared Views is refused and logged — a truncated or emptied
	// checkout must never silently delete the declared estate. Human-invoked
	// apply (CLI/API) is explicit ack and is not gated. <=0 means the 0.5
	// default; >=1 disables the guard.
	MaxPruneFraction float64
}

// Run reconciles until ctx ends.
func (c *Controller) Run(ctx context.Context) error {
	log := c.Log.With("component", "desiredstate", "path", c.Path)
	interval := c.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	log.Info("desired-state reconciliation started", "interval", interval.String())
	for {
		c.reconcile(ctx, log)
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Controller) reconcile(ctx context.Context, log *slog.Logger) {
	if _, err := os.Stat(filepath.Join(c.Path, ".git")); err == nil {
		// git is exec'd, not linked (§1.4 boring; no VCS library in the
		// control plane). A checkout without an upstream (local-only dev
		// repo) has nothing to pull — reconcile it as-is, quietly.
		hasUpstream := exec.CommandContext(ctx, "git", "-C", c.Path,
			"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}").Run() == nil
		if hasUpstream {
			// --ff-only: the checkout is a read replica of the remote;
			// divergence is an operator problem to surface, not merge.
			cmd := exec.CommandContext(ctx, "git", "-C", c.Path, "pull", "--ff-only", "--quiet")
			if out, err := cmd.CombinedOutput(); err != nil {
				// Reconcile the existing checkout anyway — stale desired
				// state beats no reconciliation; the failure is logged loudly.
				log.Error("git pull failed; reconciling existing checkout", "error", err, "output", string(out))
			}
		}
	}

	decls, err := ParseDir(c.Path)
	if err != nil {
		// Fail-safe: never apply (and above all never prune) off a broken
		// read of the desired state.
		log.Error("declarations unreadable; skipping cycle", "error", err)
		return
	}

	plan, err := ComputePlan(ctx, c.Store, decls)
	if err != nil {
		log.Error("reconcile plan failed", "error", err)
		return
	}
	maxPrune := c.MaxPruneFraction
	if maxPrune <= 0 {
		maxPrune = 0.5
	}
	for kind, s := range plan.PruneStats() {
		deletes, cacTotal := s[0], s[1]
		if cacTotal > 0 && float64(deletes)/float64(cacTotal) > maxPrune {
			log.Error("refusing reconcile: plan prunes too much of the declared estate — apply explicitly (stratt apply) if intended",
				"kind", kind, "deletes", deletes, "declared", cacTotal, "maxPruneFraction", maxPrune)
			return
		}
	}

	plan, err = Apply(ctx, c.Store, decls)
	if err != nil {
		log.Error("reconcile failed", "error", err)
		return
	}
	for _, e := range plan.Entries {
		if e.Action == ActionNoop {
			continue
		}
		if e.Error != "" {
			log.Error("reconcile action failed", "kind", e.Kind, "name", e.Name, "action", string(e.Action), "error", e.Error)
			continue
		}
		log.Info("reconciled", "kind", e.Kind, "name", e.Name, "action", string(e.Action), "members", e.MemberCount)
	}
}
