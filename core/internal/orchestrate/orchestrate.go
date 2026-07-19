// Package orchestrate owns Run lifecycle on Temporal (charter §3: Temporal
// owns all lifecycle). The Phase-0 Workflow is the thesis slice (§8): resolve
// a View, execute against it as a K8s Job, project the returned facts as
// Facets with Run provenance, and record the summary.
package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/dstout-devops/stratt/core/internal/actions"
	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/core/internal/cellrouter"
	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/evidencestore"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/planstore"
	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	"github.com/dstout-devops/stratt/core/internal/policy"
	"github.com/dstout-devops/stratt/core/internal/siteproto"
	"github.com/dstout-devops/stratt/core/internal/siterelay"
	mcpcanon "github.com/dstout-devops/stratt/sdk/mcp"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// TaskQueue is the worker queue for Run Workflows. It is Cell-scoped
// (ADR-0044): main sets it to "stratt-runs-<cell>" for a named Cell so a Cell's
// worker only picks up its own Cell's Runs and cannot collide with a peer Cell
// sharing a Temporal cluster. The single-Cell default ("local") keeps the
// legacy "stratt-runs". Set once at startup before the worker/launch paths run.
var TaskQueue = "stratt-runs"

// RunInput starts one Run against a View. Actuator and Params are the Step
// fields (§2.3: Step = Actuator + content + params). A View actuation names its
// Actuator EXPLICITLY — declared, or inherited from the parent Run — never a
// platform default (ADR-0046: the spine names no tool, §1.8: always traceable);
// it is empty only for Action runs. Slices > 1 partitions the target set across
// that many parallel K8s Jobs.
type RunInput struct {
	// RunID is the pre-created Run summary id for API launches. Empty for
	// Trigger-started executions: the Workflow's first activity (EnsureRun)
	// creates the row itself (ADR-0010).
	RunID    string
	ViewName string
	Actuator string
	// Action names a Connector Action for a targetless typed operation (§2.2,
	// ADR-0031). Mutually exclusive with Actuator/ViewName — set means this Run
	// executes via RunAction, not RunAgainstView.
	Action string
	// DryRun asks a DryRunnable Action to plan without side effects (§2.2).
	DryRun bool
	Params json.RawMessage
	// FacetWriteScope is the per-Run facet FLOOR (ADR-0054): the actuation may write
	// back ONLY these Facet namespaces (intersected with the plugin's registered grant
	// at the one governor). Declared on the Step/Baseline/Trigger and inherited by
	// derived Runs. Empty admits NO facet write-back (TIGHT least-authority default).
	FacetWriteScope []string
	// ViewParams binds a parametrized View's {{.param.x}} placeholders at
	// launch (ADR-0024) — resolved by ResolveTargets before selection.
	ViewParams map[string]any
	Slices     int
	// Trigger names the Trigger that fired this Run; empty for manual/API
	// launches (§1.8 descent: Trigger → Run).
	Trigger string
	// Baseline names the Baseline whose cadence runs this check; empty for
	// everything else (§1.8 descent: Baseline → Run, ADR-0019).
	Baseline string
	// WorkflowRunID/StepName link a Run executing as one Step of a Workflow
	// back to its WorkflowRun (§1.8 descent: Workflow → Run); empty for
	// direct launches.
	WorkflowRunID string
	StepName      string
	// Principal is the launching identity (§2.5) — checked for `use` on
	// each CredentialRef at dispatch time and recorded for audit. Only the
	// id travels; never any material.
	Principal string
	// CredentialRefs are pointer names to project into execution pods.
	CredentialRefs []string
	// StayLocal marks a Run that must NOT fan out across Cells (ADR-0044 slice
	// 5): it is the recursion base case for a forwarded child Run, and it makes
	// a zero-entity View resolution a benign empty success (a peer legitimately
	// homes none of a multi-Cell View's targets). Set by the launch path when
	// the request arrives as a verified peer fan-out; always false for a direct
	// launch, so a single-Cell estate is unaffected.
	StayLocal bool
	// Plan marks a Run executing an actuation Step's PLAN verb — it produces a
	// hash-pinned saved plan (ADR-0047 §8) rather than converging.
	Plan bool
	// PlanFrom is set on a plan-PINNED Apply: the Plan Step whose digest this Apply
	// must apply. PlanDigest is that Gate-approved digest, read from core-held state
	// (the Plan Step's output). PlanFrom set + PlanDigest empty is FAIL-CLOSED (a
	// terminal error, never a silent unpinned apply of `desired` — ADR-0047 §8).
	PlanFrom   string
	PlanDigest string
}

// ResolvedTargets is what the View resolves to at dispatch time; the version
// is recorded so blast radius stays auditable (§4.3).
type ResolvedTargets struct {
	ViewVersion int64
	Targets     []actuators.Target
}

// SiteGroup is the targets that route to one execution locus (ADR-0032). The
// built-in central cluster is Site "local".
type SiteGroup struct {
	Site    string
	Targets []actuators.Target
}

// RoutedTargets is the View's Entity set partitioned by execution locus (the
// mgmt.site Facet). Groups is a DETERMINISTICALLY SORTED slice (by Site name),
// never a map — Temporal replay forbids map-range nondeterminism.
type RoutedTargets struct {
	ViewVersion int64
	Groups      []SiteGroup
}

// SiteGateway dispatches one prepared slice to a remote Site over NATS and
// awaits its terminal result, heartbeating while it blocks (ADR-0032).
// Implemented by sitegw.Gateway; nil on a hub with no Sites configured, in
// which case a Run that routes to a remote Site fails terminally.
type SiteGateway interface {
	DispatchAndAwait(ctx context.Context, req siteproto.DispatchRequest, heartbeat func()) (dispatch.Result, error)
	// StreamApply dispatches a TYPED (EE-Job) slice and drains the Site's forwarded
	// ApplyResponses (raw proto-JSON) to onResp, returning the Site Job's exit —
	// governance runs hub-side over these frames (ADR-0051 MF2).
	StreamApply(ctx context.Context, req siteproto.DispatchRequest, heartbeat func(), onResp func(json.RawMessage)) (bool, error)
	Cancel(ctx context.Context, site, runID string) error
}

// FactSet carries per-target facts keyed for projection, plus tool-declared
// Entity observations and the Step's derived outputs schema (ADR-0017).
type FactSet struct {
	// EntityFacts: entity id → facet namespace → value.
	EntityFacts map[string]map[string]json.RawMessage
	// Entities are tool-declared observations to project with Run
	// provenance (e.g. the opentofu stratt_entities output).
	Entities []actuators.EntityObservation
	// OutputsContract is the rung-2 outputs schema the tool derived, with
	// the name it registers under (empty = none).
	OutputsContract     json.RawMessage
	OutputsContractName string
	// Workspace stamps the stratt.workspace selection label (v1 binding).
	Workspace string
}

// RunOutcome is what RunAgainstView returns to a parent workflow: the Run id
// plus the per-target verdicts and drift detail a Baseline evaluation needs
// (ADR-0019). Compact by construction — facts and events never ride here.
type RunOutcome struct {
	RunID string
	// PerTarget maps target name → status (ok | changed | failed |
	// unreachable).
	PerTarget map[string]string
	// EntityByTarget resolves target names to Entity ids (View membership at
	// dispatch time).
	EntityByTarget map[string]string
	// Drift is the per-target observed-vs-expected fragments (capped,
	// redacted upstream).
	Drift map[string][]json.RawMessage
	// Outputs are an Action's typed output VALUES (ADR-0031), validated against
	// its output Contract — returned so a DAG runner can bind them into a
	// downstream Step ({{.steps.<name>.outputs.<field>}}). Empty for Actuators.
	Outputs json.RawMessage
}

// RunAgainstView is the Phase-0 Workflow. Every state transition is a
// Temporal event — the descent ladder's Workflow → Run rungs (§1.8) fall out
// of its history.
func RunAgainstView(ctx workflow.Context, in RunInput) (RunOutcome, error) {
	opts := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		HeartbeatTimeout:    time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	ctx = workflow.WithActivityOptions(ctx, opts)
	var a *Activities

	// touchedSites is the set of execution loci this Run dispatched to (ADR-0032),
	// populated once targets are routed. Captured by the cancel defer so cleanup
	// can reach every remote Site's agent, not just the hub.
	var touchedSites []string

	// Trigger-started executions have no API handler to pre-create the Run
	// summary — the Workflow owns it (ADR-0010).
	if in.RunID == "" {
		wfID := workflow.GetInfo(ctx).WorkflowExecution.ID
		if err := workflow.ExecuteActivity(ctx, a.EnsureRun, in, wfID).Get(ctx, &in.RunID); err != nil {
			return RunOutcome{}, err
		}
	}

	// Cancellation (POST /runs/{id}/cancel → Temporal CancelWorkflow, ADR-0026):
	// the Workflow is the single writer of terminal Run status, so it owns the
	// canceled transition. Activities cannot run on a canceled ctx, so cleanup
	// runs on a disconnected context — delete the Job(s) (hub + each remote
	// Site), then stamp canceled.
	defer func() {
		if in.RunID == "" || !errors.Is(ctx.Err(), workflow.ErrCanceled) {
			return
		}
		dctx, dcancel := workflow.NewDisconnectedContext(ctx)
		defer dcancel()
		dctx = workflow.WithActivityOptions(dctx, opts)
		_ = workflow.ExecuteActivity(dctx, a.CleanupRun, in.RunID, touchedSites).Get(dctx, nil)
		_ = workflow.ExecuteActivity(dctx, a.FinishRun, in, types.RunCanceled, dispatch.Result{}).Get(dctx, nil)
	}()

	// View-scoped execution authz (§2.5, ADR-0028): before ANYTHING runs, the
	// Principal must hold `runner` on the target View. This is the one chokepoint
	// every launch path funnels through — API, façade, Trigger, Baseline, and each
	// DAG Step's child Run — so the check gates them all under one model (§1.6).
	// A denial fails fast: no targets resolved, no pod spawned.
	if err := workflow.ExecuteActivity(ctx, a.CheckExecutionGrant, in).Get(ctx, nil); err != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
	}

	// Resolve targets and partition them by execution locus (ADR-0032): the
	// mgmt.site Facet routes each Entity to its Site; unset ⇒ the local cluster.
	var routed RoutedTargets
	if err := workflow.ExecuteActivity(ctx, a.ResolveTargetsBySite, in).Get(ctx, &routed); err != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
	}
	// Flat set for CollectFacts (name→entityID) and blast-radius audit.
	resolved := ResolvedTargets{ViewVersion: routed.ViewVersion}
	for _, g := range routed.Groups {
		resolved.Targets = append(resolved.Targets, g.Targets...)
		touchedSites = append(touchedSites, g.Site)
	}
	if err := workflow.ExecuteActivity(ctx, a.MarkRunning, in.RunID).Get(ctx, nil); err != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
	}

	// Resolve credential pointers and check the Principal's `use` grant
	// before anything executes (§2.5). Metadata only — material never
	// enters workflow state.
	var creds []dispatch.CredentialMount
	if err := workflow.ExecuteActivity(ctx, a.ResolveCredentials, in).Get(ctx, &creds); err != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
	}

	// Fan out one branch per (Site, slice) — parallel Jobs, each an
	// independently-retryable activity whose rung shows in the Workflow history
	// (§1.8). The slice index is GLOBAL across all Sites: the event MsgID is
	// (runID, slice, seq), so two Sites' "slice 0" would dedup-erase each
	// other's events server-side — global numbering keeps every event and Job
	// name unique (ADR-0032). Targets are disjoint, so results merge by union.
	var futures []workflow.Future
	gslice := 0
	for _, g := range routed.Groups {
		for _, chunk := range splitTargets(g.Targets, in.Slices) {
			futures = append(futures, workflow.ExecuteActivity(ctx, a.Execute, in, gslice, g.Site,
				ResolvedTargets{ViewVersion: routed.ViewVersion, Targets: chunk}, creds))
			gslice++
		}
	}
	var result dispatch.Result
	sliceResults := make([]dispatch.Result, len(futures))
	var execErr error
	for i, f := range futures {
		if err := f.Get(ctx, &sliceResults[i]); err != nil && execErr == nil {
			execErr = err
		}
	}
	if execErr != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, execErr)
	}
	result = mergeResults(sliceResults)

	var facts FactSet
	if err := workflow.ExecuteActivity(ctx, a.CollectFacts, in, resolved, result).Get(ctx, &facts); err != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
	}
	if err := workflow.ExecuteActivity(ctx, a.ProjectFacts, in.RunID, facts).Get(ctx, nil); err != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
	}

	status := types.RunSucceeded
	if !result.Succeeded {
		status = types.RunFailed
	}
	var summaryErr error
	if err := workflow.ExecuteActivity(ctx, a.FinishRun, in, status, result).Get(ctx, nil); err != nil {
		summaryErr = err
	}
	outcome := RunOutcome{RunID: in.RunID, PerTarget: result.PerTarget, Drift: result.Drift,
		EntityByTarget: map[string]string{}}
	for _, t := range resolved.Targets {
		outcome.EntityByTarget[t.Name] = t.EntityID
	}
	// §1.8: a Run whose targets FAILED must propagate that failure to the parent
	// Workflow. Recording status=failed on the Run row is NOT enough — the parent
	// step (runActuationStep) folds to succeeded unless this child returns an error,
	// so a green Workflow would hide a red Run: the exact one-click-descent trust
	// violation the charter forbids. The FinishRun error path already returns its
	// cause (finishRun); the normal per-target fold must be just as honest.
	if status == types.RunFailed && summaryErr == nil {
		summaryErr = fmt.Errorf("run %s failed: %d of %d targets failed", in.RunID, failedTargets(result.PerTarget), len(result.PerTarget))
	}
	return outcome, summaryErr
}

// failedTargets counts the per-target outcomes that fold to a Run failure
// (failed or unreachable) — the §1.8 propagation summary above.
func failedTargets(perTarget map[string]string) int {
	n := 0
	for _, st := range perTarget {
		if st == actuators.StatusFailed || st == actuators.StatusUnreachable {
			n++
		}
	}
	return n
}

func finishRun(ctx workflow.Context, a *Activities, in RunInput, status types.RunStatus, cause error) error {
	_ = workflow.ExecuteActivity(ctx, a.FinishRun, in, status, dispatch.Result{}).Get(ctx, nil)
	return cause
}

// sitesTouched is the sorted, de-duplicated set of execution loci a Run's
// targets ran at (ADR-0032) — the Run.Sites union recorded for §1.8 descent.
func sitesTouched(result dispatch.Result) []string {
	if len(result.SiteByTarget) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for _, s := range result.SiteByTarget {
		seen[s] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Activities carries the worker-side dependencies.
// PluginAction is a Connector Action provided by a plugin over the port
// (ADR-0047/0048): its host (for InvokeRaw) and the core-side dry-run capability
// (reconciled from the ActionDecl at registration, never trusted live).
type PluginAction struct {
	Host        *pluginhost.Host
	DryRunnable bool
}

// PluginActuator is an Actuator provided by a plugin over the port
// (ADR-0047/0048): its host (for ApplyRaw) and the core-side dry-run capability
// (reconciled from the Manifest at registration, never trusted live, guardian
// fix #6). Unlike an Action, an actuation Step's authz chokepoint is the
// runner-on-View grant (ADR-0028), enforced in RunAgainstView BEFORE Execute — so
// a plugin actuator may legitimately carry ZERO CredentialRefs (guardian fix #4:
// the Action path's ungated-refusal is deliberately NOT ported here).
type PluginActuator struct {
	// Host is the hub-local plugin host (nil for a Site-only plugin). A remote-Site
	// Step builds a relay-backed host on demand from Grant + PlanStore + the
	// Activities relay dialer, so the SAME governor runs hub-side over the wire
	// (ADR-0049).
	Host        *pluginhost.Host
	DryRunnable bool
	// Grant + PlanStore let executePlugin construct a Site-backed host with
	// identical governance (the grant never leaves the hub, ADR-0049 V1).
	Grant     pluginhost.Grant
	PlanStore *planstore.Store
	// JobCommand, when non-empty, marks this Actuator as the EE-JOB (subprocess)
	// transport (ADR-0051): instead of a long-lived gRPC Apply, the core dispatches
	// a K8s Job (the EE image) whose entrypoint is this command — a shim that speaks
	// the port on stdout. The dispatcher forwards the typed stream; Host.GovernStream
	// governs it hub-side with the SAME governor as the gRPC path (MF1). Host here
	// carries only the Grant (its gRPC client is nil — the Job is the transport).
	JobCommand []string
	// Image overrides the dispatcher's default EE image for this actuator's Jobs
	// (ADR-0053: mcp needs the python-bearing EE-mcp image for the sandboxed server).
	Image string
	// MCP marks the mcp EE-Job transport (ADR-0053): the core resolves the MCPServer
	// declaration + the rung-3 pin seam around the generic EE-Job dispatch — the
	// protocol lives in the stratt-mcp shim; the pinning + graph resolution stay core.
	MCP bool
}

// jobTransport reports whether this Actuator runs as an EE-Job shim (ADR-0051)
// rather than a long-lived gRPC plugin.
func (p PluginActuator) jobTransport() bool { return len(p.JobCommand) > 0 }

type Activities struct {
	Store      *graph.Store
	Dispatcher *dispatch.Dispatcher
	Bus        *events.Bus
	Authz      authz.Authorizer
	// Decider is the Policy Decision Point PORT (ADR-0072): the policy Step
	// obtains its Decision through this seam, never a concrete engine. Nil ⇒ the
	// built-in CEL provider (the default); swap for an external engine or
	// policy.Bypass to disable governance. Content-blind: the core sends controls
	// + context and acts on the Decision.
	Decider policy.Decider
	// Log is the base logger for on-demand hosts (Site relay). Nil → slog.Default().
	Log *slog.Logger
	// RelayDial yields the relay transport to one plugin at a Site's agent
	// (ADR-0049), keyed by (site, plugin-id). Nil when no plugin relay is configured
	// — a remote-Site plugin Step then fails visibly (never silently run hub-local,
	// §1.8). Set NATS-backed in strattd.
	RelayDial func(site, pluginID string) siterelay.Dialer
	// Actuators is the registry of in-tree Actuators by name (§2.3).
	Actuators map[string]actuators.Actuator
	// Actions is the registry of in-tree Connector Actions by namespaced name
	// (§2.2, ADR-0031) — the targetless typed-operation seam.
	Actions actions.Registry
	// PluginActions routes a Connector Action name to the plugin that provides it
	// over the sovereign port (ADR-0047/0048). Exclusive with the in-tree registry
	// and across plugins — main.go fails registration on a collision (§2.4).
	PluginActions map[string]PluginAction
	// PluginActuators routes an Actuator name to the plugin that provides its
	// Plan/Apply/Destroy verbs over the port (ADR-0047/0048). Exclusive with the
	// in-tree Actuators registry and across plugins — main.go fails on a collision.
	PluginActuators map[string]PluginActuator
	// Evidence seals Finding audit bundles into the object store (§2.4,
	// ADR-0029). Nil when no object store is configured — Findings then open
	// unsealed (a logged no-op), like the opentofu actuator is gated on a state
	// key.
	Evidence *evidencestore.Store
	// Sites dispatches slices to remote execution loci over NATS (§2.3,
	// ADR-0032). Nil on a hub with no Sites configured — a Run whose targets
	// route to a remote Site then fails terminally with NoSiteGateway.
	Sites SiteGateway
	// Peers launches and polls child Runs on peer Cells over their control APIs
	// (ADR-0044 slice 5) — the write-side counterpart of the cellrouter read
	// federation. Nil on a single-Cell estate; RunAcrossCells is never reached
	// there (LaunchRun sees no peers), so the nil is never dereferenced.
	Peers *cellrouter.PeerClient
}

// EnsureRun creates the Run summary row for a Trigger-started execution
// (ADR-0010): API launches pre-create theirs in the handler; schedule-fired
// Workflows start with only the declaration's launch parameters. Returns the
// new Run id.
func (a *Activities) EnsureRun(ctx context.Context, in RunInput, workflowID string) (string, error) {
	v, err := a.Store.GetView(ctx, in.ViewName)
	if err != nil {
		return "", err
	}
	run, err := a.Store.CreateRun(ctx, types.Run{
		WorkflowID: workflowID, ViewRef: "view://" + v.Name, ViewVersion: v.Version,
		TriggeredBy: in.Trigger, Baseline: in.Baseline,
		WorkflowRunID: in.WorkflowRunID, StepName: in.StepName,
	})
	if err != nil {
		return "", err
	}
	return run.ID, nil
}

// renderTarget renders one resolved Entity as an execution target. Phase-0
// target semantics: local-connection per target (see ansible.GatherFactsPlay).
func renderTarget(e types.Entity) actuators.Target {
	name := e.Labels["vcenter.name"]
	if name == "" {
		name = e.ID
	}
	return actuators.Target{
		EntityID: e.ID,
		Name:     name,
		Vars:     map[string]string{"ansible_connection": "local"},
	}
}

// ResolveTargets resolves the View to its live Entity set and renders
// execution targets (locus-agnostic; used by the targetless/legacy paths).
func (a *Activities) ResolveTargets(ctx context.Context, in RunInput) (ResolvedTargets, error) {
	v, ents, err := a.Store.ResolveView(ctx, in.ViewName, in.ViewParams, 0)
	if err != nil {
		return ResolvedTargets{}, err
	}
	if len(ents) == 0 {
		return ResolvedTargets{}, fmt.Errorf("orchestrate: view %s resolves to zero entities", in.ViewName)
	}
	out := ResolvedTargets{ViewVersion: v.Version}
	for _, e := range ents {
		out.Targets = append(out.Targets, renderTarget(e))
	}
	return out, nil
}

// ResolveTargetsBySite resolves the View and partitions its targets by
// execution locus (the mgmt.site Facet; unset ⇒ the built-in "local" central
// cluster). Read-only — it only READS mgmt.site, never writes it (§1.2). Groups
// come back sorted by Site name so the Workflow's fan-out is replay-deterministic
// (ADR-0032).
func (a *Activities) ResolveTargetsBySite(ctx context.Context, in RunInput) (RoutedTargets, error) {
	v, ents, err := a.Store.ResolveView(ctx, in.ViewName, in.ViewParams, 0)
	if err != nil {
		return RoutedTargets{}, err
	}
	if len(ents) == 0 {
		// A forwarded child Run (StayLocal) that homes none of a multi-Cell
		// View's targets is a benign empty success, not a failure — the parent
		// RunAcrossCells scatters to ALL peers and most legitimately have zero
		// local targets (ADR-0044 slice 5). A direct launch still fails loudly.
		if in.StayLocal {
			return RoutedTargets{ViewVersion: v.Version}, nil
		}
		return RoutedTargets{}, fmt.Errorf("orchestrate: view %s resolves to zero entities", in.ViewName)
	}
	ids := make([]string, len(ents))
	for i, e := range ents {
		ids[i] = e.ID
	}
	locs, err := a.Store.FacetValuesByEntities(ctx, "mgmt.site", ids)
	if err != nil {
		return RoutedTargets{}, err
	}
	bySite := map[string][]actuators.Target{}
	for _, e := range ents {
		site := types.LocalSite
		if raw, ok := locs[e.ID]; ok {
			var loc struct {
				Site string `json:"site"`
			}
			if err := json.Unmarshal(raw, &loc); err == nil && loc.Site != "" {
				site = loc.Site
			}
		}
		bySite[site] = append(bySite[site], renderTarget(e))
	}
	names := make([]string, 0, len(bySite))
	for s := range bySite {
		names = append(names, s)
	}
	sort.Strings(names)
	// Site→Cell binding (ADR-0044 slice 6): a Site's dispatch work-queue lives on
	// its Cell's NATS, so this daemon can only reach Sites homed to its OWN Cell.
	// A target routed to a Site homed elsewhere is a loud, terminal misroute —
	// never a silently-dropped or wrongly-dispatched slice (§1.8). Cross-Cell
	// targets are handled by RunAcrossCells scatter (slice 5), which re-resolves
	// the View per Cell so a child Run only ever sees its own Cell's Sites; this
	// check is the enforced invariant behind that assumption. LocalSite is the
	// daemon's in-Cell central locus and is always reachable.
	daemonCell := a.Store.Cell()
	for _, s := range names {
		if s == types.LocalSite {
			continue
		}
		st, err := a.Store.GetSite(ctx, s)
		if err != nil {
			return RoutedTargets{}, fmt.Errorf("orchestrate: resolve site %q for cell check: %w", s, err)
		}
		if !siteReachableFromCell(st.Cell, daemonCell) {
			return RoutedTargets{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("site %q is homed to cell %q but this Run runs in cell %q; its dispatch queue lives on another Cell's NATS and is unreachable here (ADR-0044 slice 6)", s, st.Cell, daemonCell),
				"SiteCellMisroute", nil)
		}
	}
	out := RoutedTargets{ViewVersion: v.Version}
	for _, s := range names {
		out.Groups = append(out.Groups, SiteGroup{Site: s, Targets: bySite[s]})
	}
	return out, nil
}

// siteReachableFromCell reports whether a Site homed to siteCell can be reached
// from a daemon running in daemonCell (ADR-0044 slice 6). A Site's dispatch
// work-queue lives on its Cell's NATS, so only same-Cell Sites are reachable. An
// unset Site cell means co-located with whichever Cell declares it (reachable);
// the daemon's own Cell is reachable; any other named Cell is not — its queue is
// on another Cell's NATS. For a single-Cell 'local' estate every Site is
// reachable (all cells are "" or "local"), so this is a no-op there.
func siteReachableFromCell(siteCell, daemonCell string) bool {
	return siteCell == "" || siteCell == daemonCell
}

// MarkRunning transitions the Run summary to running.
func (a *Activities) MarkRunning(ctx context.Context, runID string) error {
	return a.Store.SetRunStatus(ctx, runID, types.RunRunning, nil)
}

// CheckExecutionGrant enforces View-scoped execution authz (§2.5, ADR-0028):
// the launching Principal must hold `runner` on the target View. Denial is a
// TERMINAL data problem (the grant will not appear mid-Run), so it is a
// non-retryable error that fails the Run — the same shape as the credential
// `use` denial. An empty Principal is denied outright (deny-by-default). This
// activity is invoked first in RunAgainstView, so it gates every launch path
// (API, façade, Trigger, Baseline, and each DAG Step's child) identically.
func (a *Activities) CheckExecutionGrant(ctx context.Context, in RunInput) error {
	if in.Principal == "" {
		a.audit(ctx, "", types.AuditExecGrant, "view:"+in.ViewName, types.AuditDenied)
		return temporal.NewNonRetryableApplicationError(
			"execution requires an authenticated principal with runner on the view", "ExecutionDenied", nil)
	}
	allowed, err := a.Authz.Check(ctx, in.Principal, authz.RelationRunner, "view:"+in.ViewName)
	if err != nil {
		return err
	}
	if !allowed {
		a.audit(ctx, in.Principal, types.AuditExecGrant, "view:"+in.ViewName, types.AuditDenied)
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("principal %s lacks runner on view:%s", in.Principal, in.ViewName),
			"ExecutionDenied", nil)
	}
	// The one audit event covering EVERY run path — API, trigger, schedule,
	// baseline all funnel through here (§1.6): who ran against which View.
	a.audit(ctx, in.Principal, types.AuditExecGrant, "view:"+in.ViewName, types.AuditOK)
	return nil
}

// audit appends one action to the audit stream (§1.6, ADR-0034) — best-effort
// on a background context so a slow audit write never fails the action it
// records, and a nil Store (tests) is a no-op. Principal kind is not carried
// through activities; the id is the load-bearing field.
func (a *Activities) audit(ctx context.Context, principal, action, object, outcome string) {
	if a.Store == nil {
		return
	}
	if err := a.Store.RecordAudit(context.WithoutCancel(ctx), types.AuditEvent{
		PrincipalID: principal, Action: action, Object: object, Outcome: outcome,
	}); err != nil {
		// A failed audit write must be visible, never swallowed (§1.8).
		slog.Error("audit record failed", "action", action, "err", err)
	}
}

// ResolveCredentials turns CredentialRef names into pod-mountable pointers,
// enforcing the launching Principal's `use` grant (§2.5, use-without-read).
// Output is pure metadata: secret coordinates and projection policy — the
// kubelet resolves material at spawn; nothing here can hold it.
func (a *Activities) ResolveCredentials(ctx context.Context, in RunInput) ([]dispatch.CredentialMount, error) {
	if len(in.CredentialRefs) == 0 {
		return nil, nil
	}
	if in.Principal == "" {
		return nil, temporal.NewNonRetryableApplicationError(
			"credentialRefs require an authenticated principal", "CredentialUseDenied", nil)
	}
	out := make([]dispatch.CredentialMount, 0, len(in.CredentialRefs))
	for _, name := range in.CredentialRefs {
		allowed, err := a.Authz.Check(ctx, in.Principal, authz.RelationUser, "credential_ref:"+name)
		if err != nil {
			return nil, err
		}
		if !allowed {
			a.audit(ctx, in.Principal, types.AuditCredentialUse, "credential_ref:"+name, types.AuditDenied)
			return nil, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("principal %s lacks use on credential_ref:%s", in.Principal, name),
				"CredentialUseDenied", nil)
		}
		a.audit(ctx, in.Principal, types.AuditCredentialUse, "credential_ref:"+name, types.AuditOK)
		ref, err := a.Store.GetCredentialRef(ctx, name)
		if err != nil {
			return nil, temporal.NewNonRetryableApplicationError(err.Error(), "CredentialRefNotFound", err)
		}
		if ref.Backend != types.BackendK8sSecret {
			return nil, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("credential_ref %s: backend %s not yet implemented (ADR-0009)", name, ref.Backend),
				"BackendUnimplemented", nil)
		}
		var loc struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		}
		if err := json.Unmarshal(ref.Locator, &loc); err != nil || loc.Name == "" {
			return nil, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("credential_ref %s: invalid k8s-secret locator", name), "InvalidLocator", err)
		}
		out = append(out, dispatch.CredentialMount{
			RefName:         name,
			SecretNamespace: loc.Namespace, // "" = the Job's namespace; a mismatch fails at dispatch
			SecretName:      loc.Name,
			Injection:       ref.Injection,
		})
	}
	return out, nil
}

// Execute prepares one Step slice through its Actuator and runs it at the
// slice's execution locus (ADR-0032). The Actuator prepares the pod content on
// the HUB (one Interpreter registry); a local slice runs on the hub Dispatcher,
// a remote slice is dispatched to that Site's agent over NATS. Either way the
// same prepared JobSpec drives the same dispatch.Dispatcher.Run — no parallel
// execution stack (§1.4).
func (a *Activities) Execute(ctx context.Context, in RunInput, slice int, site string, resolved ResolvedTargets, creds []dispatch.CredentialMount) (dispatch.Result, error) {
	// A View actuation carries an EXPLICIT actuator — declared or inherited from the
	// parent Run, never a platform default (ADR-0046: the spine names no tool). Empty
	// here means an under-specified declaration slipped past validation — fail loudly.
	name := in.Actuator
	if name == "" {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
			"a View actuation requires an explicit actuator (no platform default)", "ActuatorRequired", nil)
	}
	// ── Route: a plugin-provided Actuator applies over the port; else the pod ──
	// The authz chokepoint is the runner-on-View grant (RunAgainstView, ADR-0028)
	// already enforced BEFORE this activity — NOT the Action path's credential
	// use-check — so a plugin actuation Step may carry zero creds (guardian #4).
	if pa, ok := a.PluginActuators[name]; ok {
		// Admission lint (ADR-0054 MF-2): a declared FacetWriteScope must be a SUBSET
		// of the actuator's registered facet grant. An out-of-grant entry can never
		// write back — the one governor's grant∩scope AND would silently drop it — so
		// reject LOUDLY at launch naming the offending namespace (§1.8: a mismatch is
		// diagnosed, not a silent no-op). This is the earliest choke-point that holds
		// the grant registry (a worker-side runtime property); the govern-time
		// intersection remains the non-bypassable security backstop underneath.
		if bad := firstOutsideGrant(in.FacetWriteScope, pa.Grant.FacetNamespaces); bad != "" {
			return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("facet write-scope %q is not in actuator %q's registered grant (ADR-0054)", bad, name),
				"FacetWriteScopeUngranted", nil)
		}
		if pa.MCP {
			return a.executeMCP(ctx, in, slice, pa)
		}
		if pa.jobTransport() {
			return a.executeJobPlugin(ctx, in, slice, site, resolved, creds, pa)
		}
		return a.executePlugin(ctx, in, site, resolved, creds, pa)
	}
	act, ok := a.Actuators[name]
	if !ok {
		// Unknown Actuator can never succeed — fail terminally, no retries.
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("no actuator registered as %q", name), "UnknownActuator", nil)
	}
	// In-tree pod Actuators are EFFECTFUL — they declare no read-only capability, so a
	// dry-run (e.g. a baseline check, which the platform forces read-only) can never
	// run through one. Reject rather than silently run live (§1.8). Read-only work
	// belongs to a DryRunnable plugin Actuator; this is the capability gate the baseline
	// path relies on now that it no longer switches on tool name (ADR-0046).
	if in.DryRun {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("actuator %q does not support dry-run (read-only)", name), "DryRunUnsupported", nil)
	}
	spec, err := act.Prepare(in.Params, resolved.Targets)
	if err != nil {
		// Malformed Step params are terminal too.
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidStepParams", err)
	}
	heartbeat := func() { activity.RecordHeartbeat(ctx) }

	if site == "" || site == types.LocalSite {
		// Heartbeat from the dispatch loops so Temporal can deliver cancellation
		// to a long-running Job (a canceled Run stops promptly, ADR-0026).
		res, err := a.Dispatcher.Run(ctx, in.RunID, slice, spec, act, creds, heartbeat)
		if err != nil {
			return dispatch.Result{}, err
		}
		return *res, nil
	}

	// Remote Site: the prepared spec must be remote-safe — no plain Env
	// material may cross the wire (§2.5, ADR-0032). Terminal if it isn't (e.g.
	// opentofu, which carries the state-backend credential in Env → hub-local).
	if err := spec.RemoteSafe(); err != nil {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(err.Error(), "NotRemoteSafe", err)
	}
	if a.Sites == nil {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("run targets site %q but this hub has no site gateway configured", site), "NoSiteGateway", nil)
	}
	return a.Sites.DispatchAndAwait(ctx, siteproto.DispatchRequest{
		RunID: in.RunID, Slice: slice, Site: site,
		Actuator: name, DryRun: in.DryRun, Spec: spec, Creds: creds,
	}, heartbeat)
}

// firstOutsideGrant returns the first FacetWriteScope namespace that is NOT in
// the actuator's registered facet grant, or "" if every entry is within grant
// (ADR-0054 admission lint). An empty scope is trivially within any grant — the
// tight default writes no facets, never an out-of-grant violation.
func firstOutsideGrant(scope, grant []string) string {
	for _, ns := range scope {
		found := false
		for _, g := range grant {
			if g == ns {
				found = true
				break
			}
		}
		if !found {
			return ns
		}
	}
	return ""
}

// PlanStep runs the PLAN verb of a plugin-hosted Actuator (ADR-0047 §7/§8): the
// host content-addresses + encrypts the saved plan and returns the digest — the
// pin a downstream Gate binds and a plan-pinned Apply consumes. Only a plugin
// actuator supports it (the in-tree pod Actuators do not produce a pinnable plan).
// Returns the digest; a plan that produced none (empty converge) returns "".
func (a *Activities) PlanStep(ctx context.Context, in RunInput) (string, error) {
	pa, ok := a.PluginActuators[in.Actuator]
	if !ok {
		return "", temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("actuator %q does not support the Plan verb (not a plugin actuator)", in.Actuator), "PlanUnsupported", nil)
	}
	out, err := pa.Host.Plan(ctx, pluginhost.PlanInvoke{
		Principal:      in.Principal,
		Params:         in.Params,
		CredentialRefs: in.CredentialRefs,
	})
	if err != nil {
		return "", err
	}
	return out.Digest, nil
}

// executePlugin runs one Step slice through a plugin-hosted Actuator over the
// sovereign port (ADR-0047/0048). It reuses the reviewed governance of the port
// host (ApplyRaw): targets cross LEGIBLY (never in the opaque params, guardian
// #1), Succeeded is folded CORE-SIDE from per-target statuses (#3), and NOTHING
// is projected here — the returned dispatch.Result flows to CollectFacts →
// ProjectFacts, the single batched Run-provenance writer (#2). The View-grant is
// the authz chokepoint (already enforced in RunAgainstView), so zero creds is
// legitimate — the Action path's ungated-refusal is NOT ported (#4).
// surfaceRejections turns governor rejections (dropped land-grabs / confused-
// deputy targets a plugin tried to write outside its grant) into first-class
// §1.8 signals rather than a swallowed Warn (enterprise-readiness GOV-3): each
// rejection is published as a `governance-rejected` RunEvent on the descent
// stream AND recorded as a tracked, closeable governance Finding. A hostile or
// misconfigured plugin's overreach is now visible on one-click descent and on the
// Findings/compliance surface, not just in a log nobody reads. Best-effort: a
// failed event/Finding write is logged, never fatal to the Run (the write-back was
// already correctly refused by the governor — this is the visibility layer).
func (a *Activities) surfaceRejections(ctx context.Context, runID, source, plugin string, rejections []pluginhost.Rejection) {
	if len(rejections) == 0 {
		return
	}
	lg := a.Log
	if lg == nil {
		lg = slog.Default()
	}
	for _, r := range rejections {
		lg.Warn("plugin emission rejected (governance)",
			"source", source, "plugin", plugin, "kind", r.Kind, "detail", r.Detail, "reason", r.Reason)
		payload := map[string]any{
			"source": source, "plugin": plugin,
			"kind": r.Kind, "detail": r.Detail, "reason": r.Reason,
		}
		if a.Bus != nil && runID != "" {
			if err := a.Bus.Publish(ctx, types.RunEvent{RunID: runID, Kind: "governance-rejected", Payload: payload}); err != nil {
				lg.Warn("publish governance-rejected RunEvent failed", "error", err)
			}
		}
		if a.Store != nil {
			detail, _ := json.Marshal(payload)
			// Keyed by (run, plugin, kind, detail) so repeated identical overreach
			// within a Run folds to one open Finding (idempotent per offending target).
			target := fmt.Sprintf("%s/%s/%s/%s", runID, plugin, r.Kind, r.Detail)
			if err := a.Store.WriteGovernanceFinding(ctx, "governance/plugin-rejection", target, "warning", "governance/plugin-rejection", detail); err != nil {
				lg.Warn("write governance-rejection Finding failed", "error", err)
			}
		}
	}
}

func (a *Activities) executePlugin(ctx context.Context, in RunInput, site string, resolved ResolvedTargets, creds []dispatch.CredentialMount, pa PluginActuator) (dispatch.Result, error) {
	// Dry-run refused CORE-SIDE from the reconciled capability, never delegated —
	// a plugin that silently ignored dry_run would run live side effects (#6).
	if in.DryRun && !pa.DryRunnable {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("actuator %q does not support dry-run", in.Actuator), "DryRunUnsupported", nil)
	}
	// Resolve WHERE the plugin runs. Hub-local uses the pre-dialed host; a remote
	// Site builds a relay-backed host on demand (ADR-0049): the SAME grant (never
	// leaves the hub, V1) with a client that tunnels to the Site agent over the
	// outbound leaf. Governance still runs hub-side over the plugin's raw shapes.
	host := pa.Host
	if site != "" && site != types.LocalSite {
		if a.RelayDial == nil {
			return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("actuator %q targets Site %q but no plugin relay is configured", in.Actuator, site), "NoPluginRelay", nil)
		}
		log := a.Log
		if log == nil {
			log = slog.Default()
		}
		// MF-C (ADR-0052): a relay-backed host at an untrusted Site NEVER attaches hub
		// Secret coordinates to the Envelope — a remote plugin gets ref names alone.
		host = pluginhost.New(a.Store, siterelay.NewClient(a.RelayDial(site, pa.Grant.PluginIdentity)), pa.Grant, log).
			UsePlanStore(pa.PlanStore).WithoutCredentialCoordinates()
		// F1 (ADR-0049): validate the RELAYED Manifest against the hub-held grant
		// before any verb — the Site controls manifest.plugin_id, so a compromised
		// agent relaying a different plugin is rejected hub-side. Bounded-trust holds
		// until end-to-end plugin auth lands.
		if err := host.ValidateManifest(ctx); err != nil {
			return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("actuator %q at Site %q: %v", in.Actuator, site, err), "SitePluginIdentityMismatch", nil)
		}
	}
	if host == nil {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("actuator %q has no host for locus %q (Site-only plugin invoked hub-local?)", in.Actuator, site), "NoPluginHost", nil)
	}
	// Only the use-checked, authorized names cross the wire (§2.5); the plugin
	// resolves material against its own broker, confined to these.
	names := make([]string, 0, len(creds))
	for _, c := range creds {
		names = append(names, c.RefName)
	}
	// The core-resolved target set crosses LEGIBLY (#1): name + connection vars.
	// (identity_keys for write-back re-correlation are resolved by the projection
	// path today; passing them to identity-rendering actuators is a follow-up.)
	targets := make([]pluginhost.ApplyTarget, 0, len(resolved.Targets))
	for _, t := range resolved.Targets {
		targets = append(targets, pluginhost.ApplyTarget{Name: t.Name, Vars: t.Vars})
	}
	// Plan-pinned Apply (ADR-0047 §8): a Step that names a Plan source MUST carry a
	// Gate-approved digest. FAIL CLOSED on an empty digest — never a silent unpinned
	// live apply of `desired`. When present, the core fetches + RE-HASHES the plan
	// from its store (verify-don't-trust) and hands the plugin the verified bytes.
	var pinnedPlan []byte
	if in.PlanFrom != "" {
		if in.PlanDigest == "" {
			return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("actuator %q: plan-pinned Apply of %q has no approved plan digest — refusing an unpinned apply (ADR-0047 §8, fail-closed)", in.Actuator, in.PlanFrom),
				"PlanPinMissing", nil)
		}
		var verr error
		pinnedPlan, verr = host.VerifyPinnedPlan(ctx, in.PlanDigest)
		if verr != nil {
			return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("actuator %q: pinned plan %s failed verification at the Apply boundary: %v", in.Actuator, in.PlanDigest, verr),
				"PlanPinVerifyFailed", verr)
		}
	}
	activity.RecordHeartbeat(ctx) // canceled Run stops promptly (ADR-0026)
	raw, err := host.ApplyRaw(ctx, pluginhost.ApplyInvoke{
		Principal:       in.Principal,
		Params:          in.Params,
		Targets:         targets,
		DryRun:          in.DryRun,
		CredentialRefs:  names,
		PlanDigest:      in.PlanDigest,
		PinnedPlan:      pinnedPlan,
		FacetWriteScope: in.FacetWriteScope,
	})
	if err != nil {
		return dispatch.Result{}, err
	}
	// Surface governance rejections (dropped land-grabs / confused-deputy targets)
	// as first-class §1.8 signals — a RunEvent on the descent stream AND a tracked
	// Finding — never a swallowed log line (enterprise-readiness GOV-3).
	a.surfaceRejections(ctx, in.RunID, "apply", in.Actuator, raw.Rejections)
	// Map the governed, UNPROJECTED result to dispatch.Result. CollectFacts →
	// ProjectFacts perform the single batched projection with Run provenance (#2).
	res := dispatch.Result{Succeeded: raw.Succeeded, PerTarget: raw.PerTarget, Drift: raw.Drift}
	for _, e := range raw.WriteBack {
		res.Entities = append(res.Entities, actuators.EntityObservation{
			Kind: e.Kind, IdentityKeys: e.IdentityKeys, Labels: e.Labels})
	}
	// A rung-2 DerivedContract (tofu outputs schema) rides the existing
	// OutputsContract channel — CollectFacts names it from the Step's workspace and
	// ProjectFacts registers it, the core recomputing + pinning the hash (§1.5,
	// §2.2). The plugin's asserted schema_id/rev are advisory; the core owns naming.
	if len(raw.Derived) > 0 {
		res.OutputsContract = raw.Derived[0].Schema
	}
	return res, nil
}

// executeJobPlugin runs one Step slice through an EE-Job (subprocess) transport
// Actuator (ADR-0051): the stratt-ansible shim, baked into the EE image, speaks the
// port on stdout. The core dispatches the Job, the dispatcher forwards its typed
// stream folding NOTHING (MF1/MF2), and the SAME hub-side governor as the gRPC path
// (Host.GovernStream) gates it against the CORE-HELD resolved target set (MF4). The
// on-disk Job content is the sovereign ApplyRequest — one request shape, both
// transports. Nothing is projected here; the governed dispatch.Result flows to
// CollectFacts → ProjectFacts (the single batched Run-provenance writer, §1.2).
func (a *Activities) executeJobPlugin(ctx context.Context, in RunInput, slice int, site string, resolved ResolvedTargets, creds []dispatch.CredentialMount, pa PluginActuator) (dispatch.Result, error) {
	// Dry-run refused CORE-SIDE from the reconciled capability (MF6 — a shim that
	// silently ignored the check bit would run live side effects).
	if in.DryRun && !pa.DryRunnable {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("actuator %q does not support dry-run", in.Actuator), "DryRunUnsupported", nil)
	}
	// Where the EE-Job runs: hub-local dispatches directly; a remote Site runs the
	// shim Job AT the Site and forwards its typed stdout Site→hub (ADR-0051 MF2). A
	// remote Step with no Site gateway fails visibly (never silently hub-local, §1.8).
	remote := site != "" && site != types.LocalSite
	if remote && a.Sites == nil {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("actuator %q targets Site %q but this hub has no site gateway configured", in.Actuator, site), "NoSiteGateway", nil)
	}
	if pa.Host == nil {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("actuator %q has no governing host", in.Actuator), "NoPluginHost", nil)
	}

	// The core-resolved target set crosses LEGIBLY (MF4): name + connection vars +
	// a host.name identity key. The shim renders its inventory FROM these (never the
	// playbook's self-reported hosts) and stamps the write-back with the same identity
	// so gathered facts re-correlate to the resolved Entity hub-side.
	targets := make([]pluginhost.ApplyTarget, 0, len(resolved.Targets))
	ptargets := make([]*pluginv1.ApplyTarget, 0, len(resolved.Targets))
	for _, t := range resolved.Targets {
		ids := map[string]string{"host.name": t.Name}
		targets = append(targets, pluginhost.ApplyTarget{Name: t.Name, Vars: t.Vars, IdentityKeys: ids})
		ptargets = append(ptargets, &pluginv1.ApplyTarget{Name: t.Name, Vars: t.Vars, IdentityKeys: ids})
	}
	// Only the use-checked, authorized names cross (§2.5); material stays on the
	// kubelet secretKeyRef mounts (MF7 — one authz chokepoint, injection at the pod).
	credRefs := make([]*pluginv1.CredentialRef, 0, len(creds))
	for _, c := range creds {
		credRefs = append(credRefs, &pluginv1.CredentialRef{Name: c.RefName})
	}
	// The Job content is the sovereign ApplyRequest (proto-JSON) — the SAME shape the
	// gRPC transport sends, so one request contract serves both transports.
	applyReq := &pluginv1.ApplyRequest{
		Envelope: &pluginv1.Envelope{Principal: &pluginv1.Principal{Id: in.Principal, Kind: "user"}, Creds: credRefs},
		Desired:  &pluginv1.Payload{Bytes: in.Params},
		DryRun:   in.DryRun,
		Targets:  ptargets,
	}
	reqBytes, err := protojson.Marshal(applyReq)
	if err != nil {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidStepParams", err)
	}
	spec := actuators.JobSpec{
		Files:   map[string]string{"stratt/request.json": string(reqBytes)},
		Command: pa.JobCommand,
	}

	// Bridge: the dispatcher streams decoded ApplyResponses onto ch (folding nothing,
	// MF1); the governor drains ch on its own goroutine. A closed ch is the governor's
	// io.EOF; RunStream returning ends the stream. Governance runs hub-side over the
	// raw shapes — identical to the gRPC path.
	ch := make(chan *pluginv1.ApplyResponse, 64)
	type govResult struct {
		raw pluginhost.RawApplyResult
		err error
	}
	gov := make(chan govResult, 1)
	go func() {
		raw, gerr := pa.Host.GovernStream(ctx, pluginhost.NewChanStream(ctx, ch), targets, in.FacetWriteScope)
		gov <- govResult{raw: raw, err: gerr}
	}()
	heartbeat := func() { activity.RecordHeartbeat(ctx) }
	// ctx-aware send: on cancellation the governor stops draining ch, so a blocking
	// send would wedge the producer — fall through instead (the ctx-bound Job stream
	// ends promptly, ADR-0026).
	onResp := func(resp *pluginv1.ApplyResponse) {
		select {
		case ch <- resp:
		case <-ctx.Done():
		}
	}
	// Producer: hub-local runs the shim Job here and streams its typed stdout; a
	// remote Site runs the SAME shim Job at the Site and forwards the typed frames
	// (MF2). Either way the governor drains ch — governance is hub-side and single
	// (MF1), the Site folds nothing.
	var jobOK bool
	var rerr error
	if remote {
		req := siteproto.DispatchRequest{
			RunID: in.RunID, Slice: slice, Site: site, Actuator: in.Actuator,
			DryRun: in.DryRun, Typed: true, Spec: spec, Creds: creds,
		}
		jobOK, rerr = a.Sites.StreamApply(ctx, req, heartbeat, func(b json.RawMessage) {
			resp := &pluginv1.ApplyResponse{}
			if protojson.Unmarshal(b, resp) == nil {
				onResp(resp)
			}
		})
	} else {
		jobOK, _, rerr = a.Dispatcher.RunStream(ctx, in.RunID, slice, spec, creds, heartbeat, onResp)
	}
	close(ch)
	gr := <-gov
	if rerr != nil {
		return dispatch.Result{}, rerr
	}
	if gr.err != nil {
		return dispatch.Result{}, gr.err
	}
	raw := gr.raw

	a.surfaceRejections(ctx, in.RunID, "apply", in.Actuator, raw.Rejections)
	// Map the governed, UNPROJECTED result to dispatch.Result. Succeeded folds BOTH
	// §1.8/MF5 signals: the governor's terminal fold (raw.Succeeded) AND the K8s Job
	// exit (jobOK) — a green terminal followed by a non-zero exit (OOMKill, torn
	// cleanup, shim serialize error) must read NOT-OK, restoring parity with the
	// in-tree floor (dispatch.Run's res.Succeeded = the Job exit).
	res := dispatch.Result{
		Succeeded: raw.Succeeded && jobOK, PerTarget: raw.PerTarget, Drift: raw.Drift,
		Facts: map[string]map[string]json.RawMessage{},
	}
	if remote {
		// §1.8 descent: stamp the execution locus of each governed target so the
		// Run's Sites union + one-click descent show WHERE it ran (ADR-0032). The
		// Site folded nothing, so the hub stamps from the dispatch locus.
		res.SiteByTarget = make(map[string]string, len(raw.PerTarget))
		for name := range raw.PerTarget {
			res.SiteByTarget[name] = site
		}
	}
	for _, e := range raw.WriteBack {
		// Facts re-correlate to the resolved target name via the write-back's
		// host.name identity and flow through the res.Facts (name→EntityID) channel,
		// identical to CollectFacts' in-tree path. CollectFacts drops any name NOT in
		// the core-resolved set (its byName map) — that drop IS the confused-deputy
		// floor for write-backs (the governor gates item_key but not entity identity
		// against the resolved set), so do not remove it when refactoring CollectFacts.
		name := e.IdentityKeys["host.name"]
		if name == "" {
			continue
		}
		facets := make(map[string]json.RawMessage, len(e.Facets))
		for ns, v := range e.Facets {
			facets[ns] = json.RawMessage(v)
		}
		res.Facts[name] = facets
	}
	return res, nil
}

// executeMCP dispatches an mcp Step over the EE-Job transport, keeping the §1.5/§2.2
// SEAM in the core (ADR-0053): the stratt-mcp shim speaks the protocol, but the core
// resolves the MCPServer declaration + its rev, validates call-args against the pinned
// Contract (the door check), and — for REGISTER — pins each rung-3 derived_contract
// the shim proposes at its OWN held rev (MF-2), recomputing the canonical hash (MF-4).
// A pin is never a side effect of a call: BOTH the shim (register-only emission) and
// this core loop (register-only pinning + reject on a call) gate it (MF-3).
func (a *Activities) executeMCP(ctx context.Context, in RunInput, slice int, pa PluginActuator) (dispatch.Result, error) {
	var p struct {
		Server    string          `json:"server"`
		Mode      string          `json:"mode"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(in.Params) > 0 {
		if err := json.Unmarshal(in.Params, &p); err != nil {
			return dispatch.Result{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidStepParams", err)
		}
	}
	if p.Mode == "" {
		p.Mode = "call"
	}
	if p.Server == "" || (p.Mode != "register" && p.Mode != "call") {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError("mcp: server + mode (register|call) required", "InvalidStepParams", nil)
	}
	// The seam: resolve the declared server (its rev is CORE-HELD, MF-2 — never
	// shim-chosen). The shim receives the resolved declaration in the Job content.
	server, err := a.Store.GetMCPServer(ctx, p.Server)
	if err != nil {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(fmt.Sprintf("mcp: %v", err), "MCPServerNotFound", err)
	}
	step := map[string]any{"mode": p.Mode, "server": server.Name, "rev": strconv.Itoa(server.Rev), "transport": server.Transport}
	if server.Transport == types.MCPTransportStdio {
		step["script"] = server.Script
	} else {
		step["endpoint"] = server.Endpoint
		if server.TokenRef != nil {
			step["tokenFile"] = "/runner/credentials/" + server.TokenRef.CredentialRef + "/" + server.TokenRef.Key
		}
	}
	if p.Mode == "call" {
		if p.Tool == "" {
			return dispatch.Result{}, temporal.NewNonRetryableApplicationError("mcp: call requires tool", "InvalidStepParams", nil)
		}
		name := mcpcanon.ContractName(server.Name, p.Tool)
		pin, err := a.Store.GetContract(ctx, name, server.Rev)
		if err != nil {
			return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("mcp: no pinned contract %s @ rev %d — run a register Step first (§2.2 rung 3)", name, server.Rev), "MCPNotPinned", err)
		}
		args := p.Arguments
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		// Contract at the door (§1.5, STAYS core): args validate against the pin.
		if err := contract.ValidateDocument(pin.Name, pin.Schema, args); err != nil {
			return dispatch.Result{}, temporal.NewNonRetryableApplicationError(fmt.Sprintf("mcp: arguments %v", err), "InvalidStepParams", err)
		}
		step["tool"] = p.Tool
		step["arguments"] = json.RawMessage(args)
		step["pinnedHash"] = pin.Hash
	}
	desired, err := json.Marshal(step)
	if err != nil {
		return dispatch.Result{}, err
	}
	reqBytes, err := protojson.Marshal(&pluginv1.ApplyRequest{Desired: &pluginv1.Payload{Bytes: desired}})
	if err != nil {
		return dispatch.Result{}, err
	}
	spec := actuators.JobSpec{
		Files:   map[string]string{"stratt/request.json": string(reqBytes)},
		Command: pa.JobCommand,
		Image:   pa.Image,
	}

	// Dispatch the shim + govern hub-side (the ADR-0051 bridge). The "target" is the
	// single server (per-server, not per-target — the schema_id namespace-confines it).
	targets := []pluginhost.ApplyTarget{{Name: server.Name}}
	ch := make(chan *pluginv1.ApplyResponse, 64)
	type govResult struct {
		raw pluginhost.RawApplyResult
		err error
	}
	gov := make(chan govResult, 1)
	go func() {
		raw, gerr := pa.Host.GovernStream(ctx, pluginhost.NewChanStream(ctx, ch), targets, nil)
		gov <- govResult{raw: raw, err: gerr}
	}()
	heartbeat := func() { activity.RecordHeartbeat(ctx) }
	onResp := func(resp *pluginv1.ApplyResponse) {
		select {
		case ch <- resp:
		case <-ctx.Done():
		}
	}
	jobOK, _, rerr := a.Dispatcher.RunStream(ctx, in.RunID, slice, spec, nil, heartbeat, onResp)
	close(ch)
	gr := <-gov
	if rerr != nil {
		return dispatch.Result{}, rerr
	}
	if gr.err != nil {
		return dispatch.Result{}, gr.err
	}
	raw := gr.raw

	// The rung-3 pin seam (MF-1/2/3). A register-mode Step pins each RUNG_DECLARED
	// derived_contract at the CORE-HELD rev, recomputing the canonical hash; a
	// derived_contract on a CALL is rejected (a pin is never a call side effect); a
	// non-rung-3 derived from mcp is rejected (fail-closed, never a silent auto-version).
	for _, d := range raw.Derived {
		if p.Mode != "register" {
			return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("mcp: derived_contract %s on a %s Step — a pin is never a side effect of a call (ADR-0022)", d.SchemaID, p.Mode), "MCPUnexpectedPin", nil)
		}
		if d.Rung != int32(pluginv1.DerivedContract_RUNG_DECLARED) {
			return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("mcp register: derived_contract %s rung=%d, want RUNG_DECLARED (3)", d.SchemaID, d.Rung), "MCPBadRung", nil)
		}
		hash, canonical, herr := mcpcanon.CanonicalHash(d.Schema)
		if herr != nil {
			return dispatch.Result{}, temporal.NewNonRetryableApplicationError(fmt.Sprintf("mcp: %v", herr), "InvalidToolSchema", herr)
		}
		if err := a.Store.RegisterMCPContract(ctx, d.SchemaID, server.Rev, hash, canonical); err != nil {
			if errors.Is(err, graph.ErrContractDrift) {
				return dispatch.Result{}, temporal.NewNonRetryableApplicationError(err.Error(), "ContractDrift", err)
			}
			return dispatch.Result{}, err
		}
	}
	return dispatch.Result{Succeeded: raw.Succeeded && jobOK, PerTarget: raw.PerTarget}, nil
}

// CleanupRun deletes a Run's K8s Jobs on cancellation (invoked from the
// Workflow's disconnected cleanup path). It deletes the hub's Jobs and, for
// each remote Site the Run touched, publishes a cancel so the Site's agent
// deletes its Jobs too (ADR-0032). Idempotent; a partitioned Site that misses
// the cancel relies on its agent-side Job lease as the backstop.
func (a *Activities) CleanupRun(ctx context.Context, runID string, sites []string) error {
	if err := a.Dispatcher.DeleteRunJobs(ctx, runID); err != nil {
		return err
	}
	if a.Sites == nil {
		return nil
	}
	for _, site := range sites {
		if site == "" || site == types.LocalSite {
			continue
		}
		if err := a.Sites.Cancel(ctx, site, runID); err != nil {
			return err
		}
	}
	return nil
}

// splitTargets partitions targets into at most n contiguous, non-empty
// chunks (n is clamped to [1, len(targets)]).
func splitTargets(targets []actuators.Target, n int) [][]actuators.Target {
	if n < 1 {
		n = 1
	}
	if n > len(targets) {
		n = len(targets)
	}
	if n <= 1 {
		return [][]actuators.Target{targets}
	}
	chunks := make([][]actuators.Target, 0, n)
	base, extra := len(targets)/n, len(targets)%n
	for i, off := 0, 0; i < n; i++ {
		size := base
		if i < extra {
			size++
		}
		chunks = append(chunks, targets[off:off+size])
		off += size
	}
	return chunks
}

// mergeResults unions per-slice results (targets are disjoint by
// construction). SpawnLatency reports the slowest slice — the value the §8
// gate bounds.
func mergeResults(slices []dispatch.Result) dispatch.Result {
	out := dispatch.Result{
		Succeeded:    true,
		PerTarget:    map[string]string{},
		SiteByTarget: map[string]string{},
		Facts:        map[string]map[string]json.RawMessage{},
	}
	for _, r := range slices {
		out.Succeeded = out.Succeeded && r.Succeeded
		for t, s := range r.PerTarget {
			out.PerTarget[t] = s
		}
		for t, site := range r.SiteByTarget {
			out.SiteByTarget[t] = site
		}
		for t, f := range r.Facts {
			out.Facts[t] = f
		}
		if r.SpawnLatency > out.SpawnLatency {
			out.SpawnLatency = r.SpawnLatency
		}
		out.Entities = append(out.Entities, r.Entities...)
		if len(r.OutputsContract) > 0 {
			out.OutputsContract = r.OutputsContract
		}
		for t, fragments := range r.Drift {
			if out.Drift == nil {
				out.Drift = map[string][]json.RawMessage{}
			}
			out.Drift[t] = fragments
		}
	}
	return out
}

// CollectFacts joins per-target facts back to Entity ids and carries the
// tool-declared observations through (ADR-0017).
func (a *Activities) CollectFacts(ctx context.Context, in RunInput, resolved ResolvedTargets, result dispatch.Result) (FactSet, error) {
	byName := map[string]string{}
	for _, t := range resolved.Targets {
		byName[t.Name] = t.EntityID
	}
	fs := FactSet{EntityFacts: map[string]map[string]json.RawMessage{}}
	for host, facets := range result.Facts {
		if id, ok := byName[host]; ok {
			fs.EntityFacts[id] = facets
		}
	}
	fs.Entities = result.Entities
	if len(result.OutputsContract) > 0 {
		fs.OutputsContract = result.OutputsContract
		// The workspace names the derived contract and the selection label.
		var p struct {
			Workspace string `json:"workspace"`
		}
		_ = json.Unmarshal(in.Params, &p)
		if p.Workspace != "" {
			fs.Workspace = p.Workspace
			fs.OutputsContractName = "opentofu/" + p.Workspace + ".outputs"
		}
	}
	return fs, nil
}

// ProjectFacts writes gathered facts back as Facets with Run provenance —
// the projection half of the §8 slice, via the run-provenance write path
// (§1.2, §4.3) — and projects tool-declared Entity observations plus the
// derived outputs Contract (ADR-0017).
func (a *Activities) ProjectFacts(ctx context.Context, runID string, facts FactSet) error {
	p := a.Store.RunProjector()
	prov := types.Provenance{WriterKind: types.WriterRun, WriterRef: runID, At: time.Now().UTC()}
	for entityID, facets := range facts.EntityFacts {
		for ns, value := range facets {
			if err := p.UpsertFacet(ctx, prov, entityID, ns, value); err != nil {
				return err
			}
		}
	}
	for _, obs := range facts.Entities {
		labels := map[string]string{}
		for k, v := range obs.Labels {
			labels[k] = v
		}
		if facts.Workspace != "" {
			// The v1 binding surface: downstream Views select on this
			// label (ADR-0017; parametrized Views are the follow-up).
			labels["stratt.workspace"] = facts.Workspace
		}
		ids, err := p.UpsertEntities(ctx, prov, []graph.EntityUpsert{{
			Kind: obs.Kind, IdentityKeys: obs.IdentityKeys, Labels: labels,
		}})
		if err != nil {
			if errors.Is(err, graph.ErrIdentityConflict) {
				// Surface, never merge (§1.2) — mirror the Syncer posture.
				return temporal.NewNonRetryableApplicationError(
					fmt.Sprintf("output entity identity conflict: %v", err), "IdentityConflict", err)
			}
			return err
		}
		// Project the build's topology edges (ADR-0059): resolve each target BY
		// IDENTITY, then upsert Run-provenance — an unresolved target drops the edge
		// (never vivifies a placeholder, §1.2), mirroring the Syncer's resolve.
		for _, rel := range obs.Relations {
			toID, found, err := a.Store.EntityIDByIdentity(ctx, rel.ToScheme, rel.ToValue)
			if err != nil {
				return err
			}
			if !found {
				// Surface the drop (§1.8 diagnosis-parity with the Syncer, host.go): a
				// build's declared edge whose target isn't present vanishes WITH a trace,
				// never silently — a build is ordering-sensitive (the target's build may
				// not have run yet), so the Intent→Run descent must be able to explain it.
				activity.GetLogger(ctx).Warn("build relation dropped: target not found (no vivify, §1.2)",
					"type", rel.Type, "toScheme", rel.ToScheme, "toValue", rel.ToValue, "runId", runID)
				continue
			}
			// Singular placement is a MOVE, not an add (ADR-0059 re-placement): a re-run
			// with a new target retracts this Entity's OWN stale placement edge of this
			// type before adding the new one, so it is never in two subnets at once. Scoped
			// to Run-provenance — never touches a Syncer's observed edge (§1.2 cross-source).
			// §2.4 single-owning-build: the run-scoped retract is safe from last-writer-wins
			// because the provisioning exclusive-claim gives one Intent per unit, hence one
			// owning build per Entity (see RetractRunRelationsFrom for the guard + the
			// Relation-ownership-registry follow-up that would make it structural).
			if types.IsSingularPlacement(rel.Type) {
				if err := p.RetractRunRelationsFrom(ctx, rel.Type, ids[0], toID); err != nil {
					return err
				}
			}
			if err := p.UpsertRelation(ctx, prov, rel.Type, ids[0], toID); err != nil {
				return err
			}
		}
	}
	if len(facts.OutputsContract) > 0 && facts.OutputsContractName != "" {
		sum := sha256.Sum256(facts.OutputsContract)
		if _, err := a.Store.RegisterDerivedContract(ctx, facts.OutputsContractName,
			types.RungToolDerived, hex.EncodeToString(sum[:]), facts.OutputsContract); err != nil {
			return err
		}
	}
	// Rung-3 (mcp) pinning is NOT here — it moved to executeMCP over the governed
	// derived_contract channel (ADR-0053): the seam pins each RUNG_DECLARED contract at
	// the core-held rev directly, so the old MCPTools facts channel is retired.
	return nil
}

// FinishRun records the terminal status and summary counts, then publishes
// the Run-level stream-end marker — the tail's floor arrives only after
// every slice has finished (§1.8: a floor, never a premature one).
func (a *Activities) FinishRun(ctx context.Context, in RunInput, status types.RunStatus, result dispatch.Result) error {
	counts := map[string]int{}
	for _, s := range result.PerTarget {
		counts[s]++
	}
	actuator := in.Actuator // explicit (declared/inherited); Action runs leave it empty
	slices := in.Slices
	if slices < 1 {
		slices = 1
	}
	summary := map[string]any{
		"actuator":       actuator, // which engine ran the Step (§1.8 diagnosis)
		"slices":         slices,
		"targets":        len(result.PerTarget),
		"ok":             counts[actuators.StatusOK],
		"changed":        counts[actuators.StatusChanged],
		"failed":         counts[actuators.StatusFailed],
		"unreachable":    counts[actuators.StatusUnreachable],
		"spawnLatencyMs": result.SpawnLatency.Milliseconds(), // slowest slice
	}
	if in.Action != "" {
		// An Action Run is a targetless typed operation (§2.2) — record the
		// Action (and dry-run posture) instead of a misleading actuator default.
		delete(summary, "actuator")
		summary["action"] = in.Action
		if in.DryRun {
			summary["dryRun"] = true
		}
	}
	// Audit (§1.8, §2.5): who launched, with which credential pointers —
	// names only; material has no representation anywhere in the platform.
	if in.Principal != "" {
		summary["principal"] = in.Principal
	}
	if in.Trigger != "" {
		summary["trigger"] = in.Trigger
	}
	if in.Baseline != "" {
		summary["baseline"] = in.Baseline
	}
	if len(result.Drift) > 0 {
		// The drifted targets' observed-vs-expected detail (capped at the
		// dispatch seam) — the Run-level record behind each Finding (§1.8).
		summary["drift"] = result.Drift
	}
	if in.WorkflowRunID != "" {
		summary["workflowRun"] = in.WorkflowRunID
		summary["step"] = in.StepName
	}
	if len(in.CredentialRefs) > 0 {
		summary["credentialRefs"] = in.CredentialRefs
	}
	// Where did this run (§1.8, ADR-0032): the union of loci its targets ran at.
	sites := sitesTouched(result)
	if len(sites) > 0 {
		summary["sites"] = sites
	}
	if err := a.Store.SetRunStatus(ctx, in.RunID, status, summary); err != nil {
		return err
	}
	if err := a.Store.SetRunSites(ctx, in.RunID, sites); err != nil {
		return err
	}
	// Outbound Notice on terminal failure/cancel (ADR-0027) — the outbound
	// mirror of the inbound Emitter path. Notification deliveries are
	// dispatched directly (never through this activity), so a failed delivery
	// cannot loop back into a run.failed Notice. NoticeHash dedups the publish
	// across FinishRun retries.
	if kind := noticeKindForRun(status); kind != "" {
		n := types.Notice{Kind: kind, Subject: in.RunID, Payload: map[string]any{
			"status":   string(status),
			"actuator": actuator,
			"view":     in.ViewName,
			"failed":   counts[actuators.StatusFailed] + counts[actuators.StatusUnreachable],
			"targets":  len(result.PerTarget),
		}}
		if in.Trigger != "" {
			n.Payload["trigger"] = in.Trigger
		}
		if in.Baseline != "" {
			n.Payload["baseline"] = in.Baseline
		}
		if in.Principal != "" {
			n.Payload["principal"] = in.Principal
		}
		if in.WorkflowRunID != "" {
			n.Payload["workflowRun"] = in.WorkflowRunID
			n.Payload["step"] = in.StepName
		}
		if err := a.Bus.PublishNotice(ctx, n); err != nil {
			return err
		}
	}
	// MsgID (runID/0/0) dedups the marker across FinishRun retries.
	return a.Bus.Publish(ctx, types.RunEvent{RunID: in.RunID, Kind: "stream-end"})
}

// noticeKindForRun maps a terminal Run status to its outbound Notice kind
// (ADR-0027); "" for non-notifiable terminal states (succeeded).
func noticeKindForRun(s types.RunStatus) string {
	switch s {
	case types.RunFailed:
		return types.NoticeRunFailed
	case types.RunCanceled:
		return types.NoticeRunCanceled
	}
	return ""
}
