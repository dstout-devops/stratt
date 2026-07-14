package api

import (
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func TestBuildComplianceReport(t *testing.T) {
	baselines := []types.Baseline{
		{Name: "cis-a", Framework: "cis", ViewName: "prod", Severity: "critical"},
		{Name: "cis-b", Framework: "cis", ViewName: "prod", Severity: "warning"},
		{Name: "cis-c", Framework: "cis", ViewName: "prod", Severity: "info"},
		{Name: "cis-d", Framework: "cis", ViewName: "dev", Severity: "warning"},
		{Name: "stig-x", Framework: "stig", ViewName: "prod", Severity: "warning"}, // other framework ignored
		{Name: "kernel-drift", Framework: "", ViewName: "prod", Severity: "warning"},
	}
	// cis-a and cis-d have open Findings; cis-b/cis-c pass. stig-x open must not
	// leak into the cis rollup.
	openCounts := map[string]int{"cis-a": 2, "cis-d": 1, "stig-x": 5}

	rep := buildComplianceReport("cis", "", baselines, openCounts)
	if rep.Framework != "cis" || len(rep.Views) != 2 {
		t.Fatalf("report: %+v", rep)
	}
	// Views sorted: dev, prod.
	dev, prod := rep.Views[0], rep.Views[1]
	if dev.View != "dev" || prod.View != "prod" {
		t.Fatalf("view order: %+v", rep.Views)
	}
	if prod.Controls != 3 || prod.Passing != 2 || prod.Failing != 1 {
		t.Fatalf("prod counts: %+v", prod)
	}
	if prod.Score < 0.66 || prod.Score > 0.67 {
		t.Fatalf("prod score: %v", prod.Score)
	}
	if prod.FailingControls == nil || len(*prod.FailingControls) != 1 ||
		(*prod.FailingControls)[0].Baseline != "cis-a" || (*prod.FailingControls)[0].OpenFindings != 2 {
		t.Fatalf("prod failing: %+v", prod.FailingControls)
	}
	if dev.Controls != 1 || dev.Failing != 1 || dev.Score != 0 {
		t.Fatalf("dev: %+v", dev)
	}

	// View filter narrows to one View.
	only := buildComplianceReport("cis", "dev", baselines, openCounts)
	if len(only.Views) != 1 || only.Views[0].View != "dev" {
		t.Fatalf("filtered: %+v", only.Views)
	}

	// A framework with no controls yields an empty (not nil) view list.
	empty := buildComplianceReport("pci", "", baselines, openCounts)
	if empty.Views == nil || len(empty.Views) != 0 {
		t.Fatalf("empty framework: %+v", empty.Views)
	}
}
