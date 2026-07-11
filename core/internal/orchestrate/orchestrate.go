// Package orchestrate owns Run lifecycle on Temporal (charter §3: Temporal
// owns all lifecycle). The Phase-0 Workflow is the thesis slice (§8): resolve
// a View, execute against it as a K8s Job, project the returned facts as
// Facets with Run provenance, and record the summary.
package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/dstout-devops/stratt/core/internal/actuators/ansible"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// TaskQueue is the worker queue for Run Workflows.
const TaskQueue = "stratt-runs"

// RunInput starts one Run against a View.
type RunInput struct {
	RunID    string
	ViewName string
}

// ResolvedTargets is what the View resolves to at dispatch time; the version
// is recorded so blast radius stays auditable (§4.3).
type ResolvedTargets struct {
	ViewVersion int64
	Targets     []ansible.Target
}

// FactSet carries per-target facts keyed for projection.
type FactSet struct {
	// EntityFacts: entity id → facet namespace → value.
	EntityFacts map[string]map[string]json.RawMessage
}

// RunAgainstView is the Phase-0 Workflow. Every state transition is a
// Temporal event — the descent ladder's Workflow → Run rungs (§1.8) fall out
// of its history.
func RunAgainstView(ctx workflow.Context, in RunInput) error {
	opts := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		HeartbeatTimeout:    time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	ctx = workflow.WithActivityOptions(ctx, opts)
	var a *Activities

	var resolved ResolvedTargets
	if err := workflow.ExecuteActivity(ctx, a.ResolveTargets, in).Get(ctx, &resolved); err != nil {
		return finishRun(ctx, a, in.RunID, types.RunFailed, err)
	}
	if err := workflow.ExecuteActivity(ctx, a.MarkRunning, in.RunID).Get(ctx, nil); err != nil {
		return finishRun(ctx, a, in.RunID, types.RunFailed, err)
	}

	var result dispatch.Result
	if err := workflow.ExecuteActivity(ctx, a.Execute, in.RunID, resolved).Get(ctx, &result); err != nil {
		return finishRun(ctx, a, in.RunID, types.RunFailed, err)
	}

	var facts FactSet
	if err := workflow.ExecuteActivity(ctx, a.CollectFacts, resolved, result).Get(ctx, &facts); err != nil {
		return finishRun(ctx, a, in.RunID, types.RunFailed, err)
	}
	if err := workflow.ExecuteActivity(ctx, a.ProjectFacts, in.RunID, facts).Get(ctx, nil); err != nil {
		return finishRun(ctx, a, in.RunID, types.RunFailed, err)
	}

	status := types.RunSucceeded
	if !result.Succeeded {
		status = types.RunFailed
	}
	var summaryErr error
	if err := workflow.ExecuteActivity(ctx, a.FinishRun, in.RunID, status, result).Get(ctx, nil); err != nil {
		summaryErr = err
	}
	return summaryErr
}

func finishRun(ctx workflow.Context, a *Activities, runID string, status types.RunStatus, cause error) error {
	_ = workflow.ExecuteActivity(ctx, a.FinishRun, runID, status, dispatch.Result{}).Get(ctx, nil)
	return cause
}

// Activities carries the worker-side dependencies.
type Activities struct {
	Store      *graph.Store
	Dispatcher *dispatch.Dispatcher
}

// ResolveTargets resolves the View to its live Entity set and renders
// execution targets. Phase-0 target semantics: local-connection per target
// (see ansible.GatherFactsPlay).
func (a *Activities) ResolveTargets(ctx context.Context, in RunInput) (ResolvedTargets, error) {
	v, ents, err := a.Store.ResolveView(ctx, in.ViewName, 0)
	if err != nil {
		return ResolvedTargets{}, err
	}
	if len(ents) == 0 {
		return ResolvedTargets{}, fmt.Errorf("orchestrate: view %s resolves to zero entities", in.ViewName)
	}
	out := ResolvedTargets{ViewVersion: v.Version}
	for _, e := range ents {
		name := e.Labels["vcenter.name"]
		if name == "" {
			name = e.ID
		}
		out.Targets = append(out.Targets, ansible.Target{
			EntityID: e.ID,
			Name:     name,
			Vars:     map[string]string{"ansible_connection": "local"},
		})
	}
	return out, nil
}

// MarkRunning transitions the Run summary to running.
func (a *Activities) MarkRunning(ctx context.Context, runID string) error {
	return a.Store.SetRunStatus(ctx, runID, types.RunRunning, nil)
}

// Execute dispatches the K8s Job and follows it, publishing task events.
func (a *Activities) Execute(ctx context.Context, runID string, resolved ResolvedTargets) (dispatch.Result, error) {
	content := ansible.BuildContent(ansible.GatherFactsPlay, resolved.Targets)
	res, err := a.Dispatcher.Run(ctx, runID, content)
	if err != nil {
		return dispatch.Result{}, err
	}
	return *res, nil
}

// CollectFacts joins per-target facts back to Entity ids.
func (a *Activities) CollectFacts(ctx context.Context, resolved ResolvedTargets, result dispatch.Result) (FactSet, error) {
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
	return fs, nil
}

// ProjectFacts writes gathered facts back as Facets with Run provenance —
// the projection half of the §8 slice, via the run-provenance write path
// (§1.2, §4.3).
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
	return nil
}

// FinishRun records the terminal status and summary counts.
func (a *Activities) FinishRun(ctx context.Context, runID string, status types.RunStatus, result dispatch.Result) error {
	okCount, failCount := 0, 0
	for _, r := range result.PerTarget {
		if r == "ok" {
			okCount++
		} else {
			failCount++
		}
	}
	return a.Store.SetRunStatus(ctx, runID, status, map[string]any{
		"targets":        len(result.PerTarget),
		"ok":             okCount,
		"failed":         failCount,
		"spawnLatencyMs": result.SpawnLatency.Milliseconds(),
	})
}
