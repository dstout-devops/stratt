// Package cutover is the standing double-execution guard (ADR-0087). After `stratt adopt`
// (ADR-0086) materializes an observed object into a Git Workflow, the SAME object can still
// execute at its Source (an enabled AWX schedule) AND now via Stratt. This reconciler
// continuously JOINS desired state (Workflows carrying adoptedFrom) with the PROJECTION (the
// still-live foreign executors) and opens a Finding per overlap. It reads only — it writes no
// graph state; "adopted" is DERIVED at evaluation, never stored (§1.2). It is TOOL-BLIND: the
// ansible specifics (which kind, which inverse relation, which liveness facet/path/value) come
// entirely from the Connector's manifest CutoverDescriptor (§1.4); this package switches on no
// tool name. Two false-fire axes are damped/known (§1.8): projection/Syncer lag AND Git
// desired-state read-lag; each Finding descends to BOTH ends (the adoptedFrom Workflow and the
// live foreign executor).
package cutover

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// ManifestSource is the minimal surface the reconciler needs from a Connector — just its
// manifest (for the CutoverDescriptors). pluginv1.PluginServiceClient satisfies it; a test
// fake implements only this.
type ManifestSource interface {
	GetManifest(ctx context.Context, in *pluginv1.GetManifestRequest, opts ...grpc.CallOption) (*pluginv1.GetManifestResponse, error)
}

const (
	// baselineName keys the cutover Findings — a stable programmatic Baseline name (like the
	// break-glass governance Findings; no CaC Baseline doc required).
	baselineName = "adopt-cutover"
	// framework tags the Findings for filtering + GC (vocabulary-linter: ansible-cutover).
	framework = "ansible-cutover"
	severity  = "warning"
)

// Descriptor is a Connector's declaration of what "still executing at the Source" means for
// one adopted kind — the tool-blind input the reconciler reads from the plugin manifest.
type Descriptor struct {
	TargetKind        string
	Relation          string
	LivenessNamespace string
	LivenessPath      string
	LivenessValue     string
}

// Store is the reconciler's read/write surface (graph.Store satisfies it; a fake in tests).
type Store interface {
	ListWorkflows(ctx context.Context) ([]types.Workflow, error)
	EntityIDByIdentity(ctx context.Context, scheme, value string) (string, bool, error)
	RelationSources(ctx context.Context, toID, relType string) ([]string, error)
	GetFacets(ctx context.Context, entityID string) ([]types.Facet, error)
	WriteGovernanceFinding(ctx context.Context, baseline, target, severity, framework string, detail []byte) error
	ResolveClearedFindingsByFramework(ctx context.Context, framework string, keep [][2]string) (int64, error)
}

// Reconciler periodically flags adopted objects still executing at their Source.
type Reconciler struct {
	Store    Store
	Clients  []ManifestSource // Connectors whose manifests carry descriptors
	Interval time.Duration
	Log      *slog.Logger
}

// Run sweeps on Interval until ctx is cancelled.
func (rc *Reconciler) Run(ctx context.Context) error {
	if rc.Interval <= 0 {
		rc.Interval = time.Minute
	}
	t := time.NewTicker(rc.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := rc.Sweep(ctx); err != nil && rc.Log != nil {
				rc.Log.Error("cutover sweep failed; keeping previous findings", "error", err)
			}
		}
	}
}

// Descriptors fetches the CutoverDescriptors from every registered Connector manifest — the
// reconciler learns the relation/facet names ONLY from the plugin (§1.4). An unreachable
// plugin is skipped (best-effort; it re-appears next sweep).
func (rc *Reconciler) Descriptors(ctx context.Context) map[string]Descriptor {
	out := map[string]Descriptor{}
	for _, c := range rc.Clients {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		resp, err := c.GetManifest(cctx, &pluginv1.GetManifestRequest{})
		cancel()
		if err != nil {
			if rc.Log != nil {
				rc.Log.Warn("cutover: a Connector manifest was unreachable; skipping its descriptors this sweep", "error", err)
			}
			continue
		}
		for _, d := range resp.GetManifest().GetCutover() {
			out[d.GetTargetKind()] = Descriptor{
				TargetKind:        d.GetTargetKind(),
				Relation:          d.GetRelation(),
				LivenessNamespace: d.GetLivenessNamespace(),
				LivenessPath:      d.GetLivenessPath(),
				LivenessValue:     d.GetLivenessValue(),
			}
		}
	}
	return out
}

// Sweep runs one join pass: for each adopted Workflow whose target kind has a descriptor,
// open a Finding for every still-live foreign executor; resolve Findings that no longer hold.
func (rc *Reconciler) Sweep(ctx context.Context) error {
	byKind := rc.Descriptors(ctx)
	if len(byKind) == 0 {
		return nil // no Connector declares a cutover descriptor; nothing to check
	}
	workflows, err := rc.Store.ListWorkflows(ctx)
	if err != nil {
		return err
	}

	var keep [][2]string
	for _, w := range workflows {
		af := w.AdoptedFrom
		if af == nil {
			continue
		}
		d, ok := byKind[af.Kind]
		if !ok {
			continue
		}
		tid, found, err := rc.Store.EntityIDByIdentity(ctx, af.Kind, af.Identity)
		if err != nil {
			return err
		}
		if !found {
			continue // the adopted target isn't projected (retracted) — no live-at-source overlap
		}
		execIDs, err := rc.Store.RelationSources(ctx, tid, d.Relation)
		if err != nil {
			return err
		}
		for _, execID := range execIDs {
			live, execName, err := rc.executorLive(ctx, execID, d)
			if err != nil {
				return err
			}
			if !live {
				continue
			}
			detail, _ := json.Marshal(map[string]any{
				"reason":      "adopted object still executing at its Source (double-execution, ADR-0087)",
				"adoptedFrom": af,       // the Git side (descent)
				"workflow":    w.Name,   // the Stratt Named Kind now owning execution
				"executor":    execName, // the live foreign executor to disable
				"executorId":  execID,
				"namespace":   d.LivenessNamespace,
			})
			// Target = the still-firing executor entity (what the operator must disable).
			if err := rc.Store.WriteGovernanceFinding(ctx, baselineName, execID, severity, framework, detail); err != nil {
				return err
			}
			keep = append(keep, [2]string{baselineName, execID})
		}
	}
	_, err = rc.Store.ResolveClearedFindingsByFramework(ctx, framework, keep)
	return err
}

// executorLive reports whether the executor entity's liveness facet still means "firing",
// plus its human name for the Finding.
func (rc *Reconciler) executorLive(ctx context.Context, execID string, d Descriptor) (bool, string, error) {
	facets, err := rc.Store.GetFacets(ctx, execID)
	if err != nil {
		return false, "", err
	}
	for _, f := range facets {
		if f.Namespace != d.LivenessNamespace {
			continue
		}
		name := execName(f.Value)
		at, ok := valueAtPath(f.Value, d.LivenessPath)
		if !ok {
			return false, name, nil
		}
		return jsonEqual(at, d.LivenessValue), name, nil
	}
	return false, "", nil
}

// execName pulls a human "name" from the executor facet (best-effort).
func execName(facet json.RawMessage) string {
	var m struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(facet, &m)
	return m.Name
}

// valueAtPath walks a dotted path into a JSON document, returning the sub-value.
func valueAtPath(doc json.RawMessage, path string) (json.RawMessage, bool) {
	if path == "" {
		return doc, true
	}
	var cur any
	if json.Unmarshal(doc, &cur) != nil {
		return nil, false
	}
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	raw, err := json.Marshal(cur)
	if err != nil {
		return nil, false
	}
	return raw, true
}

// jsonEqual compares the facet value at the path to the descriptor's liveness value (a JSON
// literal, e.g. "true"). Falls back to a raw string compare if the literal is not valid JSON.
func jsonEqual(at json.RawMessage, want string) bool {
	var a, b any
	if json.Unmarshal(at, &a) != nil {
		return false
	}
	if json.Unmarshal([]byte(want), &b) != nil {
		return strings.TrimSpace(string(at)) == want
	}
	return reflect.DeepEqual(a, b)
}
