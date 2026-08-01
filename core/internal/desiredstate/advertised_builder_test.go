package desiredstate

import (
	"os"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/provision"
	"github.com/dstout-devops/stratt/types"
)

// Every advertised provisioning Workflow in the repo satisfies the launch-interface rules, whether
// or not any Intent currently reaches it.
//
// THIS EXISTS BECAUSE THE LOAD-TIME CHECK GOT NARROWER AND SOMETHING FELL OUT OF ITS SHADOW.
// checkAdvertisedWorkflow runs per (Intent, reachable builder) pair, so a builder is only checked
// by way of an Intent that could resolve to it. That was harmless while every Intent was in force
// in every environment — the union covered every declared builder — and it stopped being harmless
// the moment types.Intent gained an `environments` filter. Scoping the estate's two Intent/Compute
// declarations to [dev, vsphere-dc] left awsec2's `compute-build` reachable from NO Intent in the
// repo, and therefore checked by nothing: not the approval gate (§5 Flow 1), not the hardcoded
// correlation label, not the hardcoded instance literal, not required-inputs-suppliable.
//
// That is exactly the class of defect PRV-1 was — `compute-build` shipping a launch interface no
// Intent could satisfy, invisible because the one thing exercising it was a divergent second copy.
// Narrowing the per-Intent check was right (it must ask the builder that will actually run); losing
// the per-BUILDER floor was an accident of how the check was reached.
//
// So this asks the other question. Not "can this Intent build?" but "is this advertised Workflow
// launchable AT ALL by the reconcile that advertises it?" — with a SYNTHETIC Intent shaped to the
// builder rather than to the estate, so the answer does not depend on which Intents happen to
// exist today. A provider advertising a builder is making a promise; this is the floor under it.
func TestEveryAdvertisedBuilderIsLaunchableByAnIntentShapedForIt(t *testing.T) {
	if _, err := os.Stat(estateRoot); err != nil {
		t.Skipf("reference estate not found at %s (%v)", estateRoot, err)
	}
	decls, err := ParseDir(estateRoot, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	byName := map[string]types.Workflow{}
	for _, w := range decls.Workflows {
		byName[w.Name] = w
	}

	// kind → workflow, for both halves of the provisioning contract.
	type advert struct{ kind, wf, by, verb string }
	var ads []advert
	collect := func(owner string, provisions, decommissions map[string]string) {
		for k, wf := range provisions {
			ads = append(ads, advert{k, wf, owner, "build"})
		}
		for k, wf := range decommissions {
			ads = append(ads, advert{k, wf, owner, "tear down"})
		}
	}
	for _, a := range decls.Actuators {
		collect("actuator/"+a.Name, a.Provisions, a.Decommissions)
	}
	for _, c := range decls.Connectors {
		collect("connector/"+c.Name, c.Provisions, c.Decommissions)
	}
	if len(ads) == 0 {
		t.Fatal("the reference estate advertises no provisioning builders — this test would pass vacuously")
	}

	seen := map[string]bool{}
	for _, ad := range ads {
		wf, ok := byName[ad.wf]
		if !ok {
			continue // the dangling-target check owns this case, with a better message
		}
		t.Run(ad.wf+"/"+ad.verb, func(t *testing.T) {
			in, supplied, unit := probeIntent(ad.kind, wf)
			// The synthetic Intent declares exactly the params this builder binds out of the opaque
			// map — derived from the shipped scanner, so a builder that reads three params gets an
			// Intent that declares three. Any refusal left is a property of the WORKFLOW.
			for _, missing := range unsuppliedParams(wf, in) {
				in.Spec["params"].(map[string]any)[missing] = "probe"
			}
			what := "advertised by " + ad.by + ": workflow " + ad.wf
			if ad.verb == "tear down" {
				supplied = provision.TeardownLaunchParams(in.Name,
					provision.Instance{Name: teardownProbeName(supplied), Intent: in.Name, Ordinal: 1},
					"provider.identity", "probe")
			}
			if err := checkAdvertisedWorkflow(what, wf, in, supplied, unit, ad.verb); err != nil {
				t.Errorf("%v\n\nNo Intent in this repo currently reaches this Workflow, so nothing "+
					"else would have caught it. A provider that advertises a builder promises the "+
					"reconcile can launch it.", err)
			}
		})
		seen[ad.wf] = true
	}

	// The point of the test, stated as an assertion: a builder no Intent reaches is still covered.
	// If this ever goes quiet it means the advertisement itself disappeared, which is a different
	// change and should be a visible one.
	if !seen["compute-build"] {
		t.Error("compute-build is no longer advertised by any provider — the builder this test was " +
			"written for is gone; check that was intended rather than an accident")
	}
}

// probeIntent builds the smallest valid Intent of `kind` that the reconcile could launch `wf` for,
// plus the launch params it would send. Shaped to the BUILDER, deliberately not to the estate.
func probeIntent(kind string, wf types.Workflow) (types.Intent, map[string]any, string) {
	full := "Intent/" + kind
	spec := map[string]any{
		"projectKind": strings.ToLower(kind),
		"labels":      map[string]any{"probe": "true"},
		"requires":    []any{"provisioning"},
		"params":      map[string]any{},
	}
	in := types.Intent{Name: "probe-" + strings.ToLower(kind), Kind: full, Spec: spec}
	if types.SingletonIntentKinds[full] {
		sin, err := provision.FromSingletonIntent(in)
		if err != nil {
			// Cannot happen for the shape above; a panic here would hide the reason.
			return in, map[string]any{}, "singleton"
		}
		return in, provision.SingletonLaunchParams(sin,
			provision.Instance{Name: provision.SingletonKey(full, in.Name)}), "singleton"
	}
	spec["count"], spec["namePrefix"] = 1, "probe"
	pin, err := provision.FromIntent(in)
	if err != nil {
		return in, map[string]any{}, "instance"
	}
	return in, provision.BuildLaunchParams(pin, provision.Instance{
		Name: provision.InstanceName("probe", 1, 1), Intent: in.Name, Ordinal: 1,
	}), "instance"
}
