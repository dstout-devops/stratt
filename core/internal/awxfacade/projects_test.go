package awxfacade

import (
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func ansibleProject() types.Actuator {
	return types.Actuator{
		Name: "ansible-platform-baseline", ContentDir: "ansible/platform-baseline",
		FacetNamespaces: []string{"os.hardening"},
		Content: map[string]string{
			"web-server-configure.yml":  "- hosts: all\n",
			"site.yaml":                 "- hosts: all\n",
			"roles/base/tasks/main.yml": "- name: x\n",
			"group_vars/all.yml":        "k: v\n",
			"README.md":                 "notes\n",
		},
	}
}

// A manual project is the FAITHFUL rendering, not a degraded one: Stratt resolves content at estate
// parse and carries it in the JobSpec, so nothing clones at run time by design (ADR-0134 D3).
// Reporting an scm_url would describe a fetch that never happens.
func TestProjectIsManualBecauseNothingClones(t *testing.T) {
	got := actuatorToProject(ansibleProject())
	if got["scm_type"] != "" || got["scm_url"] != "" {
		t.Errorf("a content root is a directory, not a clone target: %v", got)
	}
	if got["local_path"] != "ansible/platform-baseline" {
		t.Errorf("local_path must be the declared contentDir, got %v", got["local_path"])
	}
	if got["status"] != "never updated" {
		t.Errorf("AWX's own value for a manual project that never ran an SCM update; %v claims "+
			"an update happened", got["status"])
	}
	// Empty because core tracks no per-content-root revision (AWX-001) — not because none exists.
	// A plausible substitute here would be a fact invented about a repo.
	if got["scm_revision"] != "" {
		t.Errorf("scm_revision = %v — core tracks none, and inventing one is worse than saying nothing",
			got["scm_revision"])
	}
}

// The write ceiling is the fact an operator most needs when reading a project (ADR-0134 D2: a
// per-project ceiling is the reason one Actuator maps to one project).
func TestProjectCarriesItsWriteCeiling(t *testing.T) {
	sf := actuatorToProject(ansibleProject())["summary_fields"].(map[string]any)["stratt"].(map[string]any)
	ceiling := sf["write_ceiling"].([]string)
	if len(ceiling) != 1 || ceiling[0] != "os.hardening" {
		t.Errorf("write ceiling missing: %v", sf)
	}
	files := sf["files"].([]string)
	if len(files) != 5 {
		t.Fatalf("every resolved file belongs in the listing, got %v", files)
	}
	for i := 1; i < len(files); i++ {
		if files[i-1] > files[i] {
			t.Fatalf("the file list must be sorted — a listing that reorders between calls reads as "+
				"a change: %v", files)
		}
	}
}

// Core does not parse play content (ADR-0117 D6), so "which file is a playbook" is unanswerable
// here. Root-level YAML is the honest approximation — and it must not sweep in roles/ or
// group_vars/, which would offer a client files it can never launch.
func TestPlaybookListingIsRootYAMLOnly(t *testing.T) {
	cases := map[string]bool{
		"web-server-configure.yml":  true,
		"site.yaml":                 true,
		"roles/base/tasks/main.yml": false,
		"group_vars/all.yml":        false,
		"README.md":                 false,
		".yml":                      false, // an extension with no name is not a file to offer
		"a/b.yaml":                  false,
	}
	for path, want := range cases {
		if got := isRootYAML(path); got != want {
			t.Errorf("isRootYAML(%q) = %v, want %v", path, got, want)
		}
	}
}

// An Actuator with no content root is NOT a project, and a job_template pointing at one would
// dangle — the same rule the workflow nodes hold.
func TestJobTemplateProjectLinkOnlyWhenTheActuatorHasContent(t *testing.T) {
	wf := types.Workflow{Name: "baseline", Steps: []types.Step{
		{Name: "run", Actuator: "ansible-platform-baseline", ViewName: "web-hosts",
			Params: map[string]any{"playbook": "site.yaml"}},
	}}
	step, _ := singleActuationStep(wf)

	// With a project.
	projects := map[string]int64{"ansible-platform-baseline": awxID("ansible-platform-baseline")}
	got := workflowToJobTemplate(wf, step, projects)
	if got["project"] != awxID("ansible-platform-baseline") {
		t.Errorf("project = %v, want the content root's id", got["project"])
	}
	rel := got["related"].(map[string]any)
	if rel["project"] == nil {
		t.Error("a job_template with a project must link to it")
	}
	if got["summary_fields"].(map[string]any)["project"] == nil {
		t.Error("awxkit reads the project name off summary_fields")
	}

	// Without one — a gRPC Actuator mounts no content and has no project.
	bare := workflowToJobTemplate(wf, step, map[string]int64{})
	if bare["project"] != nil {
		t.Errorf("project = %v — an Actuator with no content root has no project, and a synthesized "+
			"id would dangle", bare["project"])
	}
	if _, present := bare["related"].(map[string]any)["project"]; present {
		t.Error("no project means no link to one")
	}
}
