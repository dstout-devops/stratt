// Package provision is the provisioning-from-Intent reconcile (ADR-0058): it
// compares an Intent/Compute's desired count against the compute Entities already
// PROJECTED for it and surfaces GATED builds for the shortfall — it never builds,
// never auto-launches (§5 Flow 1), and never writes an Entity for anything unbuilt
// (§1.2). The planner here is pure: given the declared Intents and the set of
// already-built instance names, it returns what to surface, what has converged,
// and what must pause for blast-radius review (§4.3). The controller turns that
// into gated Findings; the graph is never a home for the not-yet-built.
package provision

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/dstout-devops/stratt/types"
)

// DefaultMaxBuildBatch bounds how many builds ONE reconcile will surface for a
// single Intent/Compute before it PAUSES for batch review (§4.3 blast-radius,
// ADR-0058 M4). A brand-new small fleet provisions freely; a count 3->50 edit
// (47 builds) or a namePrefix churn pauses pending explicit approval — never a
// silent fan-out.
const DefaultMaxBuildBatch = 25

// ComputeSpec is the decoded Intent/Compute payload (contracts/intents/compute.schema.json).
type ComputeSpec struct {
	Count         int               `json:"count"`
	NamePrefix    string            `json:"namePrefix"`
	ProjectKind   string            `json:"projectKind"`
	Labels        map[string]string `json:"labels"`
	Builder       string            `json:"builder"`
	BuildWorkflow string            `json:"buildWorkflow"`
	Params        map[string]any    `json:"params"`
	MaxDelta      float64           `json:"maxDelta"` // 0 => use the controller cap
}

// Intent pairs a declaration name with its decoded compute spec.
type Intent struct {
	Name string
	Spec ComputeSpec
}

// FromIntent decodes a types.Intent (kind Intent/Compute) into a provision.Intent.
func FromIntent(in types.Intent) (Intent, error) {
	raw, err := json.Marshal(in.Spec)
	if err != nil {
		return Intent{}, fmt.Errorf("provision: intent %q: marshal spec: %w", in.Name, err)
	}
	var s ComputeSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		return Intent{}, fmt.Errorf("provision: intent %q: decode compute spec: %w", in.Name, err)
	}
	return Intent{Name: in.Name, Spec: s}, nil
}

// Instance is one desired unit derived from an Intent (namePrefix + ordinal). Its
// Name is the stratt.intent/instance correlation key — the ONLY identity the
// design persists, and only ever as a label on a BUILT Entity's projection.
type Instance struct {
	Name    string
	Intent  string
	Ordinal int
}

// Pause is an Intent whose missing-count exceeds the max-delta gate: the reconcile
// surfaces ONE batch Finding, not per-instance builds, pending explicit approval (§4.3).
type Pause struct {
	Intent  string
	Missing int
	Desired int
	Limit   int
}

// Result is the pure output. It writes NOTHING: the caller turns ToBuild into
// gated Findings and Resolved into Finding resolutions. Nothing here is an Entity
// for the unbuilt (§1.2) — Instance is a derived name, recomputed every reconcile.
type Result struct {
	ToBuild  []Instance
	Resolved []Instance
	Paused   []Pause
}

// InstanceName is the stable identity: namePrefix + zero-padded ordinal, width
// driven by count so ordering is lexical (web-01..web-10, not web-1..web-10).
func InstanceName(prefix string, ordinal, count int) string {
	width := len(fmt.Sprintf("%d", count))
	if width < 2 {
		width = 2
	}
	return fmt.Sprintf("%s-%0*d", prefix, width, ordinal)
}

// desired enumerates an Intent's desired instances in deterministic order.
func desired(in Intent) []Instance {
	out := make([]Instance, 0, in.Spec.Count)
	for i := 1; i <= in.Spec.Count; i++ {
		out = append(out, Instance{Name: InstanceName(in.Spec.NamePrefix, i, in.Spec.Count), Intent: in.Name, Ordinal: i})
	}
	return out
}

// Plan computes the provisioning shortfall. `built` is the set of
// stratt.intent/instance labels already projected (correlated built Entities);
// `cap` bounds the per-Intent build batch (§4.3, 0 => DefaultMaxBuildBatch), and
// a spec.maxDelta below it tightens further. Pure: no writes, no phantom
// Entities. Two Intents deriving the same instance name is a compile error
// (§2.4 exclusive claim, M3), never a silent tiebreak.
func Plan(intents []Intent, built map[string]bool, cap int) (Result, error) {
	if cap <= 0 {
		cap = DefaultMaxBuildBatch
	}
	sorted := append([]Intent(nil), intents...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	// Exclusive claim across ALL Intents (§2.4).
	claim := map[string]string{}
	for _, in := range sorted {
		for _, inst := range desired(in) {
			if prev, dup := claim[inst.Name]; dup && prev != in.Name {
				return Result{}, fmt.Errorf("provision: instance %q is claimed by both Intent/Compute %q and %q — resolve the namePrefix collision (exclusive claim, §2.4)", inst.Name, prev, in.Name)
			}
			claim[inst.Name] = in.Name
		}
	}

	var r Result
	for _, in := range sorted {
		var missing []Instance
		for _, inst := range desired(in) {
			if built[inst.Name] {
				r.Resolved = append(r.Resolved, inst)
			} else {
				missing = append(missing, inst)
			}
		}
		limit := cap
		if in.Spec.MaxDelta > 0 {
			if f := int(math.Ceil(in.Spec.MaxDelta * float64(in.Spec.Count))); f < limit {
				limit = f
			}
		}
		if len(missing) > limit {
			// §4.3: too large a delta to fan out unattended — pause for review.
			r.Paused = append(r.Paused, Pause{Intent: in.Name, Missing: len(missing), Desired: in.Spec.Count, Limit: limit})
			continue
		}
		r.ToBuild = append(r.ToBuild, missing...)
	}
	return r, nil
}
