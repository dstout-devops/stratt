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
	"strconv"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// DefaultMaxBuildBatch bounds how many builds ONE reconcile will surface for a
// single Intent/Compute before it PAUSES for batch review (§4.3 blast-radius,
// ADR-0058 M4). A brand-new small fleet provisions freely; a count 3->50 edit
// (47 builds) or a namePrefix churn pauses pending explicit approval — never a
// silent fan-out.
const DefaultMaxBuildBatch = 25

// Placement is the desired topology placement of a provisioned unit (ADR-0059
// decision 5): the subnet it sits in and, optionally, the zone. Its CaC home is the
// Intent — a Relation cannot be declared in Git (§1.2) — and the build honors it,
// projecting the placed-in edge (Run-provenance) from the reality it creates. When
// declared placement diverges from a built host's OBSERVED placement, the reconcile
// raises a placement-drift Finding (S5, §1.8) — never a silent reconcile edit.
// Fields are DISTINCT per topology kind (decision 3): no generic `zone` string —
// that would force the build to disambiguate the edge type (in-dmz vs in-az) by
// resolving the target's kind, re-introducing the generic-zone discriminator §1.1
// forbids. dmz → an in-dmz edge to a dmz Entity; availabilityZone → an in-az edge to
// an availability-zone Entity.
type Placement struct {
	Subnet           string `json:"subnet,omitempty"`
	Dmz              string `json:"dmz,omitempty"`
	AvailabilityZone string `json:"availabilityZone,omitempty"`
}

// ComputeSpec is the decoded Intent/Compute payload (contracts/intents/compute.v3.schema.json).
// v3 (ADR-0110): the provider-coupled Builder/BuildWorkflow fields are gone — the Intent names the
// `provisioning` capability CLASS via Requires, and the reconcile resolves the concrete provider +
// its build Workflow (§1.5).
type ComputeSpec struct {
	Count       int               `json:"count"`
	NamePrefix  string            `json:"namePrefix"`
	ProjectKind string            `json:"projectKind"`
	Labels      map[string]string `json:"labels"`
	Requires    []string          `json:"requires"` // capability classes (ADR-0110); must include "provisioning"
	Params      map[string]any    `json:"params"`
	MaxDelta    float64           `json:"maxDelta"`  // 0 => use the controller cap
	Placement   *Placement        `json:"placement"` // optional desired topology placement
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

// Excess returns the BUILT instances of an Intent that are no longer desired — the count-down teardown
// set (ADR-0114 D4): built correlation names matching this Intent's `<prefix>-<ordinal>` scheme whose
// ordinal exceeds the current count. Returned ORDINAL-DESCENDING so a deterministic, exclusive selection
// tears down the highest-ordinal instances first (web-05, web-04 …) — never a §2.4 tiebreak over which
// instance dies. Pure; the caller pairs each name with its Entity identity to build the gated teardown.
func Excess(in Intent, built map[string]bool) []Instance {
	desiredSet := map[string]bool{}
	for _, d := range desired(in) {
		desiredSet[d.Name] = true
	}
	var out []Instance
	for name := range built {
		if desiredSet[name] {
			continue
		}
		if ord, ok := instanceOrdinal(in.Spec.NamePrefix, name); ok {
			out = append(out, Instance{Name: name, Intent: in.Name, Ordinal: ord})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ordinal > out[j].Ordinal })
	return out
}

// instanceOrdinal parses a built correlation name of the form "<prefix>-<digits>" and returns its
// ordinal. Reports false when the name does not belong to this prefix's fleet (so a differently-prefixed
// Intent's instances are never mis-attributed).
func instanceOrdinal(prefix, name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, prefix+"-")
	if !ok {
		return 0, false
	}
	ord, err := strconv.Atoi(rest)
	if err != nil || ord < 1 {
		return 0, false
	}
	return ord, true
}

// ── Named-singleton provisioning (ADR-0059 decision 4) ──────────────────────
// Network/topology Intents (subnet, dns-record, dmz) are cardinality-1 named
// singletons, not count/ordinal fleets. The desired unit is the ONE named Entity;
// the correlation key is (intentKind, name) — a per-kind namespace, NOT the
// stratt.intent/instance label (a subnet named "web-dmz" must never collide with a
// Compute instance named "web-dmz", §2). §4.3 bites on the number of singleton
// builds surfaced per reconcile pass (a 500-record DNS-zone import pauses the batch),
// keyed on build count, not ordinal count.

// SingletonSpec is the decoded named-singleton Intent payload
// (contracts/intents/{subnet,vlan,dmz}.v2.schema.json). v2 (ADR-0110): the provider-coupled
// Builder/BuildWorkflow fields are gone — the Intent names the `provisioning` capability CLASS via
// Requires, and the reconcile resolves the concrete provider + its build Workflow (§1.5).
type SingletonSpec struct {
	ProjectKind string            `json:"projectKind"`
	Labels      map[string]string `json:"labels"`
	Requires    []string          `json:"requires"` // capability classes (ADR-0110); must include "provisioning"
	Params      map[string]any    `json:"params"`
	Placement   *Placement        `json:"placement"` // optional desired topology placement
}

// SingletonIntent pairs a declaration name + its Intent kind with the decoded spec.
type SingletonIntent struct {
	Name string
	Kind string // types.IntentSubnet | IntentDnsRecord | IntentDmz
	Spec SingletonSpec
}

// SingletonKey is the per-kind correlation key (intentKind, name) — the value of the
// stratt.intent/singleton label a built singleton Entity carries, so desired<->built
// correlates without a cross-kind collision.
func SingletonKey(kind, name string) string { return kind + "/" + name }

// FromSingletonIntent decodes a types.Intent (a singleton kind) into a SingletonIntent.
func FromSingletonIntent(in types.Intent) (SingletonIntent, error) {
	raw, err := json.Marshal(in.Spec)
	if err != nil {
		return SingletonIntent{}, fmt.Errorf("provision: intent %q: marshal spec: %w", in.Name, err)
	}
	var s SingletonSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		return SingletonIntent{}, fmt.Errorf("provision: intent %q: decode singleton spec: %w", in.Name, err)
	}
	return SingletonIntent{Name: in.Name, Kind: in.Kind, Spec: s}, nil
}

// PlanSingletons computes the named-singleton shortfall. `built` is the set of
// stratt.intent/singleton correlation keys already projected. Each desired Instance
// carries its correlation key as Name (kind/name) so the claim + built maps namespace
// by kind. Two Intents claiming the same (kind, name) is a compile error (§2.4). If the
// TOTAL missing across the pass exceeds cap, the whole batch pauses (§4.3), never a
// silent fan-out. Pure: no writes, no phantom Entities.
func PlanSingletons(intents []SingletonIntent, built map[string]bool, cap int) (Result, error) {
	if cap <= 0 {
		cap = DefaultMaxBuildBatch
	}
	sorted := append([]SingletonIntent(nil), intents...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Kind != sorted[j].Kind {
			return sorted[i].Kind < sorted[j].Kind
		}
		return sorted[i].Name < sorted[j].Name
	})

	// Exclusive claim on (kind, name) across ALL singleton Intents (§2.4).
	claim := map[string]string{}
	for _, in := range sorted {
		key := SingletonKey(in.Kind, in.Name)
		if prev, dup := claim[key]; dup {
			return Result{}, fmt.Errorf("provision: singleton %q is claimed by two Intents (%q and %q) — resolve the name collision (exclusive claim, §2.4)", key, prev, in.Name)
		}
		claim[key] = in.Name
	}

	var r Result
	var missing []Instance
	for _, in := range sorted {
		key := SingletonKey(in.Kind, in.Name)
		inst := Instance{Name: key, Intent: in.Name}
		if built[key] {
			r.Resolved = append(r.Resolved, inst)
		} else {
			missing = append(missing, inst)
		}
	}
	// §4.3: pause the whole batch if the pass would surface too many builds at once.
	if len(missing) > cap {
		r.Paused = append(r.Paused, Pause{Intent: "(singletons)", Missing: len(missing), Desired: len(sorted), Limit: cap})
		return r, nil
	}
	r.ToBuild = missing
	return r, nil
}

// ── Placement drift (ADR-0059 decision 5, S5 / §1.8) ────────────────────────
// A unit's DECLARED placement (its Intent's placement.subnet) can diverge from its
// OBSERVED placement (the subnet it is actually placed-in, per a Syncer's edge). The
// reconcile surfaces that as a placement-drift Finding — the desired-vs-observed gap is
// diagnosable, never silently wrong. Converging it (re-placing a live host) is a gated
// move Workflow, a separate slice; until then the Finding is the signal.

// Drift is one placement divergence. A unit drifts only when it has BOTH a declared
// placement AND an observed placement, and the declared subnet is not among the
// observed ones — an un-placed or un-declared unit is simply not compared.
type Drift struct {
	Unit     string   // correlation value (instance name / singleton key)
	Declared string   // the Intent's placement.subnet (a subnet's canonical net.subnet.name)
	Observed []string // the subnet name(s) the unit is actually placed-in
}

// DetectPlacementDrift pairs declared placements (unit → declared subnet) with observed
// placements (unit → observed subnet names) and returns the units whose declared subnet
// is not among the observed. Pure, deterministic order.
func DetectPlacementDrift(declared map[string]string, observed map[string][]string) []Drift {
	var out []Drift
	for unit, want := range declared {
		obs, ok := observed[unit]
		if !ok || len(obs) == 0 {
			continue // not yet placed / not observed — no drift signal
		}
		found := false
		for _, o := range obs {
			if o == want {
				found = true
				break
			}
		}
		if !found {
			out = append(out, Drift{Unit: unit, Declared: want, Observed: obs})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Unit < out[j].Unit })
	return out
}

// DeclaredComputePlacements maps each desired instance name → its Intent's
// placement.subnet, for the compute Intents that declare one. The correlation values are
// stratt.intent/instance labels — the observed side keys on the same. Pure.
func DeclaredComputePlacements(intents []Intent) map[string]string {
	out := map[string]string{}
	for _, in := range intents {
		if in.Spec.Placement == nil || in.Spec.Placement.Subnet == "" {
			continue
		}
		for _, inst := range desired(in) {
			out[inst.Name] = in.Spec.Placement.Subnet
		}
	}
	return out
}

// DeclaredSingletonPlacements maps each singleton correlation key → its placement.subnet.
// Keys are stratt.intent/singleton labels — the observed side keys on the same. Pure.
func DeclaredSingletonPlacements(intents []SingletonIntent) map[string]string {
	out := map[string]string{}
	for _, in := range intents {
		if in.Spec.Placement == nil || in.Spec.Placement.Subnet == "" {
			continue
		}
		out[SingletonKey(in.Kind, in.Name)] = in.Spec.Placement.Subnet
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
