package desiredstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuilderParamsAnIntentCannotSupplyAreRefused pins the load-time half of "this Intent can
// actually be built" (ADR-0146 D4).
//
// Two checks already existed on either side of this one and neither could see it. The reconcile's
// generated launch keys are checked against the builder's declared `inputs`; the builder's declared
// inputs are checked for being bound by some Step. But `params` is PROVIDER-SHAPED and opaque to
// core (§1.5), so nothing types its contents — and template.Substitute fails CLOSED on a missing
// field. The result is a launch that fails AFTER an operator approves the gate, which is the exact
// §1.8 shape this package refuses everywhere else.
//
// It was not hypothetical: `app-tier` declared only `params.tier` while compute-build binds region,
// instanceType and ami, so the reference estate's one placed-in-a-subnet host had never been
// buildable. The reference estate is the assertion's other half — TestStagedEstateParses and
// TestReferenceEstateProvisioningIntent both fail if it regresses — so this test covers the
// synthetic direction: that the refusal happens, and names the param and the Intent.
func TestBuilderParamsAnIntentCannotSupplyAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		params  string
		wantErr string
	}{
		{
			name:    "missing the param the builder binds",
			params:  "    tier: app\n",
			wantErr: "launch.params.region",
		},
		{
			name:   "declaring it satisfies the builder",
			params: "    tier: app\n    region: us-east-1\n",
		},
		{
			// The Intent may declare MORE than the builder binds — params are provider-shaped and
			// a second provider for the same kind legitimately reads different keys. Only the
			// missing direction is an error.
			name:   "extra params are not an error",
			params: "    tier: app\n    region: us-east-1\n    unusedByThisBuilder: x\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeParamsEstate(t, dir, tc.params)
			_, err := ParseDir(dir, nil)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("expected the estate to load, got: %v", err)
			case tc.wantErr == "":
				return
			case err == nil:
				t.Fatal("a builder binding a param the Intent does not declare must be refused at " +
					"load — otherwise it fails at launch, after the approval")
			}
			for _, want := range []string{tc.wantErr, "probe-fleet", "§1.5"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("diagnostic should mention %q; got %v", want, err)
				}
			}
		})
	}
}

// writeParamsEstate lays down the minimum that reaches checkAdvertisedWorkflow: a provisioning
// Actuator advertising a builder, that builder, and one Intent/Compute with the given params.
func writeParamsEstate(t *testing.T, dir, params string) {
	t.Helper()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "views"), 0o755); err != nil { // views/ is non-optional
		t.Fatal(err)
	}
	write("actuators/prober.yaml",
		"name: prober\naddress: probe:9090\npluginIdentity: probe\ntier: trusted\n"+
			"provides: [provisioning]\nlabelKeys: [stratt.intent/instance]\n"+
			"provisions: {Compute: probe-build}\n")
	write("workflows/probe-build.yaml",
		"name: probe-build\n"+
			"inputs:\n  type: object\n  additionalProperties: false\n"+
			"  required: [instance, projectKind, labels, params]\n"+
			"  properties:\n"+
			"    instance: { type: string }\n"+
			"    projectKind: { type: string }\n"+
			"    labels: { type: object, additionalProperties: { type: string } }\n"+
			"    params: { type: object, additionalProperties: true }\n"+
			"steps:\n"+
			"  - name: approve\n    gate:\n      approvers:\n        teams: [platform-admins]\n"+
			"  - name: build\n    needs: [approve]\n    action: probe/create\n"+
			"    credentialRefs: [probe-cred]\n"+
			"    params:\n"+
			"      name: \"{{.launch.instance}}\"\n"+
			"      region: \"{{.launch.params.region}}\"\n"+
			"      projectKind: \"{{.launch.projectKind}}\"\n"+
			"      projectLabels: \"{{.launch.labels}}\"\n")
	write("intents/probe-fleet.yaml",
		"name: probe-fleet\nkind: Intent/Compute\nspec:\n"+
			"  count: 1\n  namePrefix: probe\n  projectKind: host\n"+
			"  requires: [provisioning]\n  params:\n"+params)
}

// TestPlacementTargetMustBeADeclaredSubnet: a placement naming a subnet nothing declares can never
// resolve — in any environment, forever — and surfaces as "build <name> first", advice nobody can
// take (ADR-0147 D4). Refused at the diff, like an unknown environment or an undeclared Actuator.
func TestPlacementTargetMustBeADeclaredSubnet(t *testing.T) {
	for _, tc := range []struct {
		name    string
		target  string
		subnets bool
		wantErr bool
	}{
		{name: "typo'd target with the real subnet declared", target: "ap-subnet", subnets: true, wantErr: true},
		{name: "target declared", target: "app-subnet", subnets: true},
		{name: "no placement at all", target: ""},
		{name: "target named but no Intent/Subnet anywhere", target: "app-subnet", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeParamsEstate(t, dir, "    tier: app\n    region: us-east-1\n")
			if tc.target != "" {
				// Re-write the Intent with placement; the builder is unchanged.
				writeFile(t, dir, "intents/probe-fleet.yaml",
					"name: probe-fleet\nkind: Intent/Compute\nspec:\n"+
						"  count: 1\n  namePrefix: probe\n  projectKind: host\n"+
						"  requires: [provisioning]\n"+
						"  placement:\n    subnet: "+tc.target+"\n"+
						"  params:\n    region: us-east-1\n")
			}
			if tc.subnets {
				writeFile(t, dir, "intents/app-subnet.yaml",
					"name: app-subnet\nkind: Intent/Subnet\nspec:\n"+
						"  projectKind: subnet\n  requires: [provisioning]\n  params:\n    cidr: 10.0.0.0/24\n")
			}
			_, err := ParseDir(dir, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatal("a placement target no Intent/Subnet declares must be refused at load")
				}
				if !strings.Contains(err.Error(), tc.target) {
					t.Errorf("the diagnostic must name the unresolvable target; got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected the estate to load, got: %v", err)
			}
		})
	}
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
