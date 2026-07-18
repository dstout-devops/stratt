package desiredstate

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"log/slog"

	"github.com/dstout-devops/stratt/core/internal/compiler"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/policy"
	"github.com/dstout-devops/stratt/core/internal/provision"
	"github.com/dstout-devops/stratt/types"
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
	// Decider is the PDP port for the admission PEP (ADR-0073): each reconcile
	// admits the estate's declarations through it, rejecting a denied load. Nil
	// ⇒ admission is skipped (no policy engine configured).
	Decider policy.Decider
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

	decls, err := ParseDir(c.Path, c.Decider)
	if err != nil {
		// Fail-safe: never apply (and above all never prune) off a broken
		// read of the desired state.
		log.Error("declarations unreadable; skipping cycle", "error", err)
		return
	}

	// Environment scope (ADR-0057): a scoped daemon applies only its slice. The
	// store's cac list reads are scoped identically, so the prune candidate set
	// and the compiler see the same slice — out-of-scope declarations are neither
	// applied nor pruned (the §1.2 data-layer partition, never a convention).
	decls = ScopeToEnvironment(decls, c.Store.ActiveEnvironment())

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

	// Garbage-collect Findings whose Entity has been tombstoned (ADR-0043):
	// a renewed cert changes identity (new serial = new Entity), so the old
	// Entity's drift Finding would otherwise linger open forever. Estate-wide,
	// idempotent, self-healing; non-fatal to the cycle.
	if n, err := c.Store.ResolveFindingsForTombstonedEntities(ctx); err != nil {
		log.Error("finding tombstone-GC failed", "error", err)
	} else if n > 0 {
		log.Info("resolved findings for tombstoned entities", "count", n)
	}

	// Cell placement check (ADR-0044): an Entity whose home Cell disagrees with
	// a Source observing it is a cross-Cell collision (the multi-master
	// condition) — surface it as a Finding, and resolve it when reconciled.
	// Estate-wide, idempotent, non-fatal; a no-op for a single-Cell estate.
	if n, err := c.Store.WriteCellPlacementFindings(ctx); err != nil {
		log.Error("cell placement check failed", "error", err)
	} else if n > 0 {
		log.Warn("cell placement mismatch findings", "count", n)
	}
	if n, err := c.Store.ResolveClearedCellPlacementFindings(ctx); err != nil {
		log.Error("cell placement resolve failed", "error", err)
	} else if n > 0 {
		log.Info("resolved reconciled cell placement findings", "count", n)
	}

	// Multi-source Facet contention (ADR-0060): when >1 source projects the same
	// (Entity, namespace) and no authority is declared, the effective scalar value
	// can't be resolved — surface it as a Finding, resolve when it clears. Non-fatal.
	if n, err := c.Store.WriteFacetContentionFindings(ctx); err != nil {
		log.Error("facet contention check failed", "error", err)
	} else if n > 0 {
		log.Warn("multi-source facet contention findings", "count", n)
	}
	if n, err := c.Store.ResolveClearedFacetContentionFindings(ctx); err != nil {
		log.Error("facet contention resolve failed", "error", err)
	} else if n > 0 {
		log.Info("resolved cleared facet contention findings", "count", n)
	}

	// Provisioning reconcile (ADR-0058): surface GATED builds for Intent/Compute
	// shortfalls (desired count vs projected+correlated Entities). It never builds
	// and never writes an Entity for the unbuilt (§1.2) — the shortfall is
	// recomputed each cycle and the provisioning Findings reconciled. Non-fatal.
	c.reconcileProvisioning(ctx, decls, log)
}

// reconcileProvisioning turns Intent/Compute declarations into gated-build
// Findings (ADR-0058). The desired count minus the already-projected+correlated
// instances is the shortfall; each missing instance becomes a provisioning
// Finding referencing the gated build Workflow the operator launches (§5 Flow 1),
// a delta beyond §4.3 pauses as one batch Finding, and instances that have since
// built resolve. Nothing here writes an Entity or persists desired state (§1.2).
func (c *Controller) reconcileProvisioning(ctx context.Context, decls Declarations, log *slog.Logger) {
	log = log.With("component", "provision")
	var intents []provision.Intent
	specs := map[string]provision.ComputeSpec{}
	var singletons []provision.SingletonIntent
	singSpecs := map[string]provision.SingletonIntent{}
	for _, in := range decls.Intents {
		switch {
		case in.Kind == types.IntentCompute:
			pi, err := provision.FromIntent(in)
			if err != nil {
				log.Error("intent/compute decode failed", "intent", in.Name, "error", err)
				return
			}
			intents = append(intents, pi)
			specs[pi.Name] = pi.Spec
		case types.SingletonIntentKinds[in.Kind]:
			si, err := provision.FromSingletonIntent(in)
			if err != nil {
				log.Error("singleton intent decode failed", "intent", in.Name, "kind", in.Kind, "error", err)
				return
			}
			singletons = append(singletons, si)
			singSpecs[in.Name] = si
		}
	}

	built, err := c.Store.ProvisionedInstances(ctx)
	if err != nil {
		log.Error("provisioned-instances read failed", "error", err)
		return
	}
	res, err := provision.Plan(intents, built, 0)
	if err != nil {
		// Exclusive-claim collision (§2.4) — a declaration error to fix, not a
		// build to run. Surface loudly; apply nothing.
		log.Error("provisioning plan rejected", "error", err)
		return
	}
	// Named-singleton mode (ADR-0059 decision 4) — the same gated-Finding plumbing.
	builtSing, err := c.Store.ProvisionedSingletons(ctx)
	if err != nil {
		log.Error("provisioned-singletons read failed", "error", err)
		return
	}
	sres, err := provision.PlanSingletons(singletons, builtSing, 0)
	if err != nil {
		log.Error("singleton provisioning plan rejected", "error", err)
		return
	}

	var keepB, keepT []string
	keep := func(b, t string) { keepB = append(keepB, b); keepT = append(keepT, t) }

	for _, inst := range res.ToBuild {
		sp := specs[inst.Intent]
		detail, _ := json.Marshal(map[string]any{
			"instance": inst.Name, "intent": inst.Intent, "ordinal": inst.Ordinal,
			"builder": sp.Builder, "buildWorkflow": sp.BuildWorkflow,
			"projectKind": sp.ProjectKind, "labels": sp.Labels, "params": sp.Params,
			"placement": sp.Placement,
			"reason":    "declared but not built — launch the gated build Workflow (never auto-run, §5 Flow 1)",
		})
		b := "provision/" + inst.Intent
		if err := c.Store.WriteProvisionFinding(ctx, b, inst.Name, "warning", detail); err != nil {
			log.Error("write provision finding failed", "instance", inst.Name, "error", err)
			continue
		}
		keep(b, inst.Name)
	}
	// Singleton builds: baseline provision/<intent>, target = the (kind/name)
	// correlation key the built Entity will carry as stratt.intent/singleton.
	for _, inst := range sres.ToBuild {
		si := singSpecs[inst.Intent]
		detail, _ := json.Marshal(map[string]any{
			"singleton": inst.Name, "intent": inst.Intent, "intentKind": si.Kind,
			"correlationLabel": map[string]string{"stratt.intent/singleton": inst.Name},
			"builder":          si.Spec.Builder, "buildWorkflow": si.Spec.BuildWorkflow,
			"projectKind": si.Spec.ProjectKind, "labels": si.Spec.Labels, "params": si.Spec.Params,
			"placement": si.Spec.Placement,
			"reason":    "declared but not built — launch the gated build Workflow (never auto-run, §5 Flow 1)",
		})
		b := "provision/" + inst.Intent
		if err := c.Store.WriteProvisionFinding(ctx, b, inst.Name, "warning", detail); err != nil {
			log.Error("write singleton provision finding failed", "singleton", inst.Name, "error", err)
			continue
		}
		keep(b, inst.Name)
	}
	for _, p := range sres.Paused {
		detail, _ := json.Marshal(map[string]any{
			"missing": p.Missing, "desired": p.Desired, "limit": p.Limit,
			"reason": "singleton provisioning batch exceeds the §4.3 max-delta gate — review, then apply explicitly",
		})
		b := "provision/" + p.Intent
		const batch = "(batch)"
		if err := c.Store.WriteProvisionFinding(ctx, b, batch, "warning", detail); err != nil {
			log.Error("write singleton batch finding failed", "error", err)
			continue
		}
		keep(b, batch)
		log.Warn("singleton provisioning paused: max-delta gate", "missing", p.Missing, "limit", p.Limit)
	}
	for _, p := range res.Paused {
		detail, _ := json.Marshal(map[string]any{
			"intent": p.Intent, "missing": p.Missing, "desired": p.Desired, "limit": p.Limit,
			"reason": "provisioning delta exceeds the §4.3 max-delta gate — review, then raise maxDelta or apply explicitly",
		})
		b := "provision/" + p.Intent
		const batch = "(batch)"
		if err := c.Store.WriteProvisionFinding(ctx, b, batch, "warning", detail); err != nil {
			log.Error("write provision batch finding failed", "intent", p.Intent, "error", err)
			continue
		}
		keep(b, batch)
		log.Warn("provisioning paused: max-delta gate", "intent", p.Intent, "missing", p.Missing, "limit", p.Limit)
	}

	resolved, err := c.Store.ResolveProvisionFindingsExcept(ctx, keepB, keepT)
	if err != nil {
		log.Error("resolve provision findings failed", "error", err)
	}
	if len(res.ToBuild) > 0 || len(res.Paused) > 0 || len(sres.ToBuild) > 0 || len(sres.Paused) > 0 || resolved > 0 {
		log.Info("provisioning reconcile",
			"toBuild", len(res.ToBuild), "paused", len(res.Paused),
			"singletonBuild", len(sres.ToBuild), "singletonPaused", len(sres.Paused), "resolved", resolved)
	}

	// Placement drift (ADR-0059 decision 5, S5): declared placement vs observed.
	c.reconcilePlacementDrift(ctx, intents, singletons, log)
}

// reconcilePlacementDrift surfaces the desired-vs-observed placement gap as Findings
// (ADR-0059 S5, §1.8): a unit whose Intent declares placement.subnet but is OBSERVED
// placed-in a different subnet drifts. Nothing here writes an Entity or edge (§1.2) —
// converging a drift (re-placing a live host) is a gated move Workflow, a separate slice;
// until then the Finding is the signal. Findings resolve when the unit converges, stops
// being observed, or its placement is withdrawn.
func (c *Controller) reconcilePlacementDrift(ctx context.Context, compute []provision.Intent, singletons []provision.SingletonIntent, log *slog.Logger) {
	declaredC := provision.DeclaredComputePlacements(compute)
	declaredS := provision.DeclaredSingletonPlacements(singletons)
	if len(declaredC) == 0 && len(declaredS) == 0 {
		// No placement declared anywhere — clear any stale drift Findings and stop.
		if _, err := c.Store.ResolvePlacementDriftFindingsExcept(ctx, nil); err != nil {
			log.Error("resolve placement findings failed", "error", err)
		}
		return
	}
	observedC, err := c.Store.ObservedPlacements(ctx, "stratt.intent/instance")
	if err != nil {
		log.Error("observed placements (instance) failed", "error", err)
		return
	}
	observedS, err := c.Store.ObservedPlacements(ctx, "stratt.intent/singleton")
	if err != nil {
		log.Error("observed placements (singleton) failed", "error", err)
		return
	}
	drifts := append(provision.DetectPlacementDrift(declaredC, observedC),
		provision.DetectPlacementDrift(declaredS, observedS)...)

	var keep []string
	for _, d := range drifts {
		detail, _ := json.Marshal(map[string]any{
			"unit": d.Unit, "declared": d.Declared, "observed": d.Observed,
			"reason": "declared placement diverges from observed placement — re-place via a gated move Workflow (never a reconcile edit, §5)",
		})
		if err := c.Store.WritePlacementDriftFinding(ctx, d.Unit, detail); err != nil {
			log.Error("write placement drift finding failed", "unit", d.Unit, "error", err)
			continue
		}
		keep = append(keep, d.Unit)
	}
	resolved, err := c.Store.ResolvePlacementDriftFindingsExcept(ctx, keep)
	if err != nil {
		log.Error("resolve placement findings failed", "error", err)
	}
	if len(drifts) > 0 || resolved > 0 {
		log.Info("placement-drift reconcile", "drifted", len(drifts), "resolved", resolved)
	}
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
