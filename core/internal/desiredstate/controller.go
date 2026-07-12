package desiredstate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"log/slog"

	"github.com/dstout-devops/stratt/core/internal/compiler"
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
	// MaxDelta is the Intent-compiler max-delta gate fraction (§4.3, ADR-0023):
	// the per-Assignment engine default when the Assignment declares no
	// override. <=0 means the compiler default (0.5).
	MaxDelta float64
	// CompileStatus, when set, receives each pass's compile summary for the
	// read-only GET /compile surface.
	CompileStatus *compiler.Status
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

	// The Intent compiler runs after the intent-layer kinds are persisted and
	// re-runs every cycle — membership drifts without Git changes (Syncer
	// relabels), so the compiled Baselines must re-derive continuously (§4.3,
	// ADR-0023).
	c.compile(ctx, log)
}

// compile derives compiled Baselines from the declared Intent/Assignment/
// Blueprint objects and live View membership, applies the result, and
// publishes the summary for GET /compile.
func (c *Controller) compile(ctx context.Context, log *slog.Logger) {
	log = log.With("component", "compiler")
	plan, err := compiler.Compile(ctx, c.Store, c.MaxDelta)
	if err != nil {
		log.Error("compile failed", "error", err)
		return
	}
	errs := plan.Apply(ctx, c.Store)
	if c.CompileStatus != nil {
		c.CompileStatus.Set(compiler.Snapshot{
			CompiledAt: time.Now().UTC(), CompiledBaselines: len(plan.Upserts),
			Errors: errs, Deltas: plan.Deltas,
		})
	}
	for _, e := range errs {
		log.Error("compile error", "error", e)
	}
	for _, d := range plan.Deltas {
		switch {
		case d.Paused:
			log.Warn("compile paused: max-delta gate", "assignment", d.Assignment, "note", d.Note)
		case len(d.Joins)+len(d.Leaves) > 0:
			log.Info("compiled", "assignment", d.Assignment, "members", d.MemberCount,
				"joins", len(d.Joins), "leaves", len(d.Leaves), "unrouted", len(d.Unrouted))
		}
	}
}
