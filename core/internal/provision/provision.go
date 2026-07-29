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
	"errors"
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
	// Zones + PerZone are the KEYED cardinality (ADR-0123 D1), exclusive with Count: instance
	// identity becomes <namePrefix>-<zone>-<ordinal> with the ordinal scoped WITHIN its zone,
	// so adding a zone adds instances and renumbers none. Under Count's positional scheme a
	// zone-list edit renumbers everything after the insertion point and the reconcile reads
	// that as destroy-and-recreate — the fleet-wide churn ADR-0058 D4 flags.
	Zones   []string `json:"zones"`
	PerZone int      `json:"perZone"`
}

// Keyed reports whether this Intent spreads by zone rather than by a flat count.
func (s ComputeSpec) Keyed() bool { return len(s.Zones) > 0 }

// DesiredCount is the total number of instances an Intent wants, whichever cardinality it
// declares. One function so the max-delta gate (§4.3) and the shortfall arithmetic cannot
// disagree about what "the fleet" is.
func (s ComputeSpec) DesiredCount() int {
	if s.Keyed() {
		return len(s.Zones) * s.PerZone
	}
	return s.Count
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
	// Zone is the availability zone this instance is keyed to, empty for a positional
	// (count-based) fleet. It is part of the instance's IDENTITY, not a label on it — which is
	// the whole point of keying (ADR-0123 D1).
	Zone string
	// SubnetRef is the PROVIDER-NATIVE identity of the placement target, resolved by the
	// reconcile from the declared Intent/Subnet name (ADR-0147 D1) via ResolveSubnetRef.
	//
	// It rides on Instance rather than being computed inside BuildLaunchParams because that
	// function is PURE by design — the whole per-instance decision with no substrate — and
	// resolving this needs a graph read. Keeping the read in the controller and the decision
	// here preserves that split; it is the same reason Zone lives here.
	SubnetRef string
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

// KeyedInstanceName renders a zone-keyed instance identity: <prefix>-<zone>-<ordinal>, with the
// ordinal zero-padded against perZone and scoped WITHIN the zone. Keyed rather than positional so
// a zone-list edit is additive: inserting a zone leaves every existing name untouched, where a
// flat ordinal would renumber everything after it (ADR-0123 D1, the Terraform for_each-over-count
// precedent).
func KeyedInstanceName(prefix, zone string, ordinal, perZone int) string {
	width := len(fmt.Sprintf("%d", perZone))
	if width < 2 {
		width = 2
	}
	return fmt.Sprintf("%s-%s-%0*d", prefix, zone, width, ordinal)
}

// desired enumerates an Intent's desired instances in deterministic order.
func desired(in Intent) []Instance {
	if in.Spec.Keyed() {
		out := make([]Instance, 0, in.Spec.DesiredCount())
		for _, z := range in.Spec.Zones {
			for i := 1; i <= in.Spec.PerZone; i++ {
				out = append(out, Instance{
					Name:   KeyedInstanceName(in.Spec.NamePrefix, z, i, in.Spec.PerZone),
					Intent: in.Name, Ordinal: i, Zone: z,
				})
			}
		}
		return out
	}
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
	// A keyed name is <prefix>-<zone>-<ordinal> (ADR-0123 D1), so the ordinal is the LAST
	// dash-separated segment. Parsing from the right handles both shapes without needing to know
	// which cardinality the Intent declares — which matters because this runs over BUILT names,
	// and a fleet mid-migration between the two has some of each.
	if i := strings.LastIndex(rest, "-"); i >= 0 {
		rest = rest[i+1:]
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
			if f := int(math.Ceil(in.Spec.MaxDelta * float64(in.Spec.DesiredCount()))); f < limit {
				limit = f
			}
		}
		if len(missing) > limit {
			// §4.3: too large a delta to fan out unattended — pause for review.
			r.Paused = append(r.Paused, Pause{Intent: in.Name, Missing: len(missing), Desired: in.Spec.DesiredCount(), Limit: limit})
			continue
		}
		r.ToBuild = append(r.ToBuild, missing...)
	}
	return r, nil
}

// InstanceLabel is the correlation label key a built instance carries so the next reconcile can
// tell desired from built (ADR-0058 M1). Named here because ADR-0120 D2 makes the build Workflow
// responsible for projecting it, and a build that projects the wrong key produces a host nobody
// asked for AND a Finding that never resolves.
const InstanceLabel = "stratt.intent/instance"

// BuildLaunchParams derives the launch inputs for ONE instance's gated build (ADR-0120 D2).
//
// This is the fix for a live defect: `count: 2` expands to web-01 and web-02 and raises two
// Findings, but the build Workflow had no typed channel to receive WHICH instance, so it hardcoded
// web-01 and the second instance could not be built through the gated path at all.
//
// Pure, and deliberately so — it is the whole per-instance decision, and the controller around it
// needs a substrate. Every value here is already computed by the reconcile; what was missing was
// somewhere typed to put it.
//
// `params` is passed through WHOLE rather than flattened into sibling inputs.
// contracts/intents/compute.v3.schema.json declares it `additionalProperties: true` — "opaque build
// params passed to the resolved provider, validated against ITS input Contract downstream (§1.5) …
// Intent/Compute never types these" — while a Workflow's `inputs` schema must be
// `additionalProperties: false`. Flattening would force every build Workflow to enumerate
// region/instanceType/ami, so adding one Intent param would break the launch until an estate
// Workflow was edited: the provider coupling ADR-0110 removed, reintroduced one layer down. Passing
// it whole also makes a key collision inexpressible — `params` is open, so `params: {instance: …}`
// is legal today, and flattened it would collide with the key below with no exclusive-claim rule to
// resolve it (§2.4).
func BuildLaunchParams(in Intent, inst Instance) map[string]any {
	// map[string]any, not map[string]string, and placement as a map rather than the struct:
	// these params are stored as jsonb and read back as map[string]any, so building them in the
	// JSON-canonical shape here means the in-memory value and the stored one behave identically.
	// template.lookup traverses map[string]any only, so a map[string]string would resolve as a
	// whole value and then fail the moment anything addressed a field inside it — a mismatch that
	// would surface far from its cause.
	labels := make(map[string]any, len(in.Spec.Labels)+1)
	for k, v := range in.Spec.Labels {
		labels[k] = v
	}
	// The correlation label is DERIVED here rather than forwarded: the compute branch of the
	// reconcile never emitted one (only the singleton branch did), so this is where it starts
	// existing. It is also the load-bearing param — the next reconcile matches on it.
	labels[InstanceLabel] = inst.Name

	out := map[string]any{
		"instance":    inst.Name,
		"ordinal":     inst.Ordinal,
		"projectKind": in.Spec.ProjectKind,
		"labels":      labels,
	}
	// Placement is emitted COMPLETE — all three fields, empty when undeclared (ADR-0123 D2).
	//
	// Fields stay DISTINCT per topology kind (ADR-0059 D3 rejected a generic `zone` string so a
	// build never disambiguates the edge type by resolving its target's kind). What D2 WITHDRAWS
	// is the other half of D3: emitting only declared fields, "so a build Workflow can tell 'no
	// zone declared' from 'zone is empty'".
	//
	// That distinction is unusable by any consumer that exists. Template substitution has no
	// conditionals (ADR-0083 D5), so a builder cannot branch on presence — it can only bind a
	// field or not. What the omission actually bought was the opposite of its intent: it made
	// {{.launch.placement.*}} unsafe in a SHARED builder, because the key vanishes for an
	// unplaced Intent and the substituter fails closed on an unknown field. Which is exactly why
	// `placement` was declared by all seven build Workflows and bound by none of them, and why
	// app-tier's declared placement reached nothing.
	place := map[string]any{"subnet": "", "subnetRef": "", "dmz": "", "availabilityZone": ""}
	if pl := in.Spec.Placement; pl != nil {
		place["subnet"] = pl.Subnet
		place["dmz"] = pl.Dmz
		place["availabilityZone"] = pl.AvailabilityZone
	}
	// The PROVIDER-NATIVE id beside the declared NAME, never instead of it (ADR-0147 D1). Both
	// are needed and they are different things: `subnet` is what Git declares and what placement
	// drift compares, `subnetRef` is what a provider's Action can actually address. A builder
	// binds the ref; nothing else should.
	//
	// Present-and-empty, like the fields above and for the same reason (ADR-0123 D2): template
	// substitution has no conditionals, so a key that vanished for an unplaced Intent would make
	// {{.launch.placement.subnetRef}} unsafe in a builder shared by placed and unplaced Intents.
	place["subnetRef"] = inst.SubnetRef
	// A keyed instance carries its OWN zone: the zone is its identity (ADR-0123 D1), so it does
	// not need declaring twice and must not be able to disagree with the name (§2.4).
	if inst.Zone != "" {
		place["availabilityZone"] = inst.Zone
	}
	out["placement"] = place
	if len(in.Spec.Params) > 0 {
		out["params"] = in.Spec.Params
	}
	return out
}

// TeardownLaunchParams derives the launch inputs for ONE excess instance's gated teardown
// (ADR-0114 D4, joining ADR-0120 D1's launch spec).
//
// The mirror of BuildLaunchParams, and deliberately in this package beside it for one reason: the
// declaration-time check on advertised teardown Workflows calls THIS function, so the keys it
// verifies a Workflow declares are the keys the reconcile actually sends. A second hand-written
// list in the validator is how that check would go quietly stale.
//
// (scheme, value) rather than a provider-named param: the identity of the thing being destroyed is
// provider-shaped and core does not know its spelling (§1.5). The scheme rides ALONG with the value
// so a teardown Workflow can assert what it was handed rather than assume — a destructive Action
// pointed at an identity from the wrong scheme is the worst failure available here. Not a nested
// object keyed by scheme: template.lookup splits a binding path on `.`, so a dotted scheme like
// `vcenter.uuid` is unaddressable inside one.
//
// `intent` and `ordinal` are sent because a teardown Workflow may legitimately want to say WHICH
// declaration shrank and where in the order this unit fell — the same descent value the build side
// gets (§1.8). They are not required inputs; a Workflow that ignores them still has to declare them,
// which is the accepted-vs-consumed gap ADR-0120 books for the keyed-placement ADR.
func TeardownLaunchParams(intent string, inst Instance, identityScheme, identityValue string) map[string]any {
	return map[string]any{
		"instance":       inst.Name,
		"intent":         intent,
		"ordinal":        inst.Ordinal,
		"identityScheme": identityScheme,
		"identityValue":  identityValue,
	}
}

// FilterToDeclared drops launch params the target Workflow does not declare (ADR-0123 D3).
//
// This is what lets a builder declare exactly what it consumes. Before it, `additionalProperties:
// false` plus "the reconcile supplies it" forced a builder to declare every param core might send,
// so an input accepted and silently dropped looked identical to a consumed one — and that is how
// `placement` came to be declared by seven build Workflows and bound by none.
//
// Core offers; the builder takes what it needs. The correlation-critical params are not optional in
// practice, but that is enforced at DECLARATION (checkAdvertisedWorkflow) where the error can name
// the file, rather than by silently sending something the launch would then reject.
func FilterToDeclared(params map[string]any, declared map[string]bool) map[string]any {
	if len(declared) == 0 {
		return params // no declared interface to filter against; the declaration check owns that case
	}
	out := make(map[string]any, len(params))
	for k, v := range params {
		if declared[k] {
			out[k] = v
		}
	}
	return out
}

// SingletonLabel is the correlation label key a built named-singleton carries (ADR-0059 D4). It is
// the ONLY key ProvisionedSingletons reads, so a build projecting any other spelling produces an
// Entity that never resolves its own Finding — which is exactly what
// estate/workflows/vsphere-subnet-build.yaml did with `stratt.intent/subnet` until ADR-0120.
const SingletonLabel = "stratt.intent/singleton"

// SingletonLaunchParams derives the launch inputs for ONE named-singleton's gated build
// (ADR-0120 D2, extended to the ADR-0059 D4 path).
//
// The singleton sibling of BuildLaunchParams, and the params differ for a real reason rather than by
// accident: a singleton has no ordinal, and its correlation key is per-kind — (intentKind, name), so
// a Subnet named "web-dmz" can never collide with a Compute instance of the same name (§2). `name`
// is carried BESIDE the key because the two are different things: the key correlates desired with
// built, while the name is what the resource is actually called in the provider.
//
// `params` passes through whole, for the reason BuildLaunchParams records.
func SingletonLaunchParams(si SingletonIntent, inst Instance) map[string]any {
	labels := make(map[string]any, len(si.Spec.Labels)+1)
	for k, v := range si.Spec.Labels {
		labels[k] = v
	}
	// inst.Name IS the correlation key (SingletonKey → "Intent/Subnet/app-subnet"), per PlanSingletons.
	labels[SingletonLabel] = inst.Name

	out := map[string]any{
		"singleton":   inst.Name,
		"name":        si.Name,
		"intentKind":  si.Kind,
		"projectKind": si.Spec.ProjectKind,
		"labels":      labels,
	}
	// Complete, like the fleet path — see BuildLaunchParams for why the omit-when-undeclared
	// shape (ADR-0059 D3) is withdrawn (ADR-0123 D2).
	place := map[string]any{"subnet": "", "subnetRef": "", "dmz": "", "availabilityZone": ""}
	if pl := si.Spec.Placement; pl != nil {
		place["subnet"], place["dmz"], place["availabilityZone"] = pl.Subnet, pl.Dmz, pl.AvailabilityZone
	}
	place["subnetRef"] = inst.SubnetRef // ADR-0147 D1 — see BuildLaunchParams
	out["placement"] = place
	if len(si.Spec.Params) > 0 {
		out["params"] = si.Spec.Params
	}
	return out
}

// ── Placement resolution (ADR-0147) ─────────────────────────────────────────────────────

// SubnetRefUnbuilt is returned when the declared placement target exists as an Intent but has not
// been built yet. It is a distinct outcome from an error: nothing is wrong with the declaration,
// the estate is simply not there yet, and the caller turns it into an OBSERVABLE Finding with no
// launch spec rather than a failure (ADR-0147 D3).
var SubnetRefUnbuilt = errors.New("placement target is declared but not built yet")

// ResolveSubnetRef translates a DECLARED placement target — an Intent/Subnet name, the only thing
// Git can hold — into the PROVIDER-NATIVE identity the resolved builder's Action requires.
//
// This is the translation whose absence made `placement.subnet` unusable: it reached the provider
// (ADR-0123 D2) carrying an Intent name, and `compute-build` bound it straight into `subnetId`, so
// RunInstances was called with "app-subnet" and the substrate answered InvalidSubnetID.NotFound.
//
// CORE STAYS CONTENT-BLIND. It never learns what `aws.subnetId` means. It intersects the identity
// schemes the built subnet actually carries with the ones the RESOLVED PROVIDER DECLARES
// (`identitySchemes` on its Actuator/Connector declaration — already the provider's declared
// identity vocabulary, ADR-0047 §1), and:
//
//   - exactly one   → that value is the ref;
//   - none          → an error naming both sides, because a provider that cannot address the
//     subnet it is being asked to build into cannot be made to by guessing;
//   - more than one → REFUSED, never a tiebreak. Two addressable identities for one placement is
//     two answers to "which id", and a rule for choosing is the implicit
//     precedence §2.4 exists to forbid.
//
// identities is the built subnet's scheme→value map (empty/nil ⇒ SubnetRefUnbuilt).
func ResolveSubnetRef(target string, identities map[string]string, providerSchemes []string) (string, error) {
	if len(identities) == 0 {
		return "", fmt.Errorf("%w: %s", SubnetRefUnbuilt, target)
	}
	var hits []string
	for _, scheme := range providerSchemes {
		if v, ok := identities[scheme]; ok {
			hits = append(hits, scheme+"="+v)
		}
	}
	sort.Strings(hits) // deterministic diagnostics; a map-range message is a coin flip
	switch len(hits) {
	case 1:
		return strings.SplitN(hits[0], "=", 2)[1], nil
	case 0:
		return "", fmt.Errorf(
			"placement target %s is built, but it carries none of the identity schemes the resolved "+
				"provider declares (it carries %v; the provider declares %v). The provider cannot "+
				"address the subnet it is being asked to build into",
			target, sortedSchemes(identities), providerSchemes)
	default:
		return "", fmt.Errorf(
			"placement target %s is addressable by more than one of the resolved provider's identity "+
				"schemes (%v) — refused rather than picked, because choosing between two ids for one "+
				"placement is a rule nobody wrote down (§2.4)", target, hits)
	}
}

func sortedSchemes(identities map[string]string) []string {
	out := make([]string, 0, len(identities))
	for k := range identities {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
