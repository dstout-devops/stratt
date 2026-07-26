package awxsim

import "encoding/json"

// The fixture structs carry AWX's own JSON field names so the awx read client
// decodes them unchanged. awxsim defines them locally (rather than importing
// the awx package) to keep the fixture free of an import cycle with the
// client's internal test.

type fJobTemplate struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	JobType       string `json:"job_type"`
	Playbook      string `json:"playbook"`
	Project       int    `json:"project"`
	Inventory     int    `json:"inventory"`
	SurveyEnabled bool   `json:"survey_enabled"`
	// Run state + knobs — read by the PROJECTION only (ADR-0128). The adopt decode struct
	// models neither, and ignores them.
	Status        string `json:"status,omitempty"`
	LastJobRun    string `json:"last_job_run,omitempty"`
	LastJobFailed bool   `json:"last_job_failed,omitempty"`
	NextJobRun    string `json:"next_job_run,omitempty"`
	Forks         int    `json:"forks,omitempty"`
	Limit         string `json:"limit,omitempty"`
	JobTags       string `json:"job_tags,omitempty"`
	BecomeEnabled bool   `json:"become_enabled,omitempty"`
	SummaryFields struct {
		Credentials []fCredSummary `json:"credentials"`
		// The PROJECTION half reads these two and the adopt half does not — awxsim
		// served neither until the Syncer got a sim it could run against. `project`
		// is the join key for the cross-source `runs` edge (<project.name>/<playbook>).
		Organization fNamed `json:"organization"`
		Project      fNamed `json:"project"`
	} `json:"summary_fields"`
}

// fNamed is AWX's summary_fields shape for a referenced object.
type fNamed struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type fCredSummary struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type fProject struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	ScmType   string `json:"scm_type"`
	ScmURL    string `json:"scm_url"`
	ScmBranch string `json:"scm_branch"`
}

type fWorkflowJT struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	SummaryFields struct {
		Organization fNamed `json:"organization"`
	} `json:"summary_fields"`
}

// The three objects ONLY the projection reads — awxsim served none of them, so the
// Syncer half could not run against it at all (its Enumerate fails the whole Observe on
// any 404). Recorded in docs/parity/awx-object-model.md as the read-path asymmetry.

type fSchedule struct {
	ID                 int    `json:"id"`
	Name               string `json:"name"`
	RRule              string `json:"rrule"`
	Enabled            bool   `json:"enabled"`
	UnifiedJobTemplate int    `json:"unified_job_template"`
	SummaryFields      struct {
		UnifiedJobTemplate struct {
			ID             int    `json:"id"`
			Name           string `json:"name"`
			UnifiedJobType string `json:"unified_job_type"`
		} `json:"unified_job_template"`
	} `json:"summary_fields"`
}

type fOrganization struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type fTeam struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	SummaryFields struct {
		Organization fNamed `json:"organization"`
	} `json:"summary_fields"`
}

type fWorkflowNode struct {
	ID                 int    `json:"id"`
	Identifier         string `json:"identifier"`
	UnifiedJobTemplate int    `json:"unified_job_template"`
	SuccessNodes       []int  `json:"success_nodes"`
	FailureNodes       []int  `json:"failure_nodes"`
	AlwaysNodes        []int  `json:"always_nodes"`
	Timeout            int    `json:"timeout"`
	SummaryFields      struct {
		UnifiedJobTemplate struct {
			ID             int    `json:"id"`
			Name           string `json:"name"`
			UnifiedJobType string `json:"unified_job_type"`
		} `json:"unified_job_template"`
	} `json:"summary_fields"`
}

type fInventory struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	HostFilter string `json:"host_filter"`
	TotalHosts int    `json:"total_hosts"`
}

type fInventorySource struct {
	ID         int             `json:"id"`
	Name       string          `json:"name"`
	Source     string          `json:"source"`
	SourceVars json.RawMessage `json:"source_vars"`
}

type fHost struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type fCredential struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type fSurveyQuestion struct {
	QuestionName        string          `json:"question_name"`
	QuestionDescription string          `json:"question_description"`
	Variable            string          `json:"variable"`
	Type                string          `json:"type"`
	Required            bool            `json:"required"`
	Default             json.RawMessage `json:"default"`
	Min                 *int            `json:"min"`
	Max                 *int            `json:"max"`
	Choices             json.RawMessage `json:"choices"`
}

type fSurveySpec struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Spec        []fSurveyQuestion `json:"spec"`
}

type estate struct {
	JobTemplates     []fJobTemplate
	WorkflowJTs      []fWorkflowJT
	WorkflowNodes    map[int][]fWorkflowNode
	Projects         []fProject
	Inventories      []fInventory
	InventorySources map[int][]fInventorySource
	Hosts            map[int][]fHost
	Credentials      []fCredential
	Surveys          map[int]fSurveySpec
	Schedules        []fSchedule
	Organizations    []fOrganization
	Teams            []fTeam
}

func iptr(n int) *int { return &n }

// seed builds the canned migration estate: a static inventory (manual hosts),
// a dynamic aws_ec2 inventory, a smart inventory with a partly-reducible
// host_filter; a git-backed job template with a survey and an SSH credential;
// a manual-project job template (no SCM content); and a workflow with an
// approval node and a success/failure fan-out.
func seed() *estate {
	e := &estate{
		WorkflowNodes:    map[int][]fWorkflowNode{},
		InventorySources: map[int][]fInventorySource{},
		Hosts:            map[int][]fHost{},
		Surveys:          map[int]fSurveySpec{},
	}

	// Inventories: 1 static, 2 dynamic (aws_ec2), 3 smart.
	e.Inventories = []fInventory{
		{ID: 1, Name: "legacy-prod", Kind: "", TotalHosts: 3},
		{ID: 2, Name: "cloud-ec2", Kind: "", TotalHosts: 12},
		{ID: 3, Name: "smart-web", Kind: "smart",
			HostFilter: "groups__name=prod and ansible_facts__ansible_distribution__family=RedHat and name__icontains=web"},
	}
	e.Hosts[1] = []fHost{
		{ID: 11, Name: "web1.legacy", Enabled: true},
		{ID: 12, Name: "web2.legacy", Enabled: true},
		{ID: 13, Name: "db1.legacy", Enabled: true},
	}
	e.InventorySources[2] = []fInventorySource{
		{ID: 21, Name: "ec2-use1", Source: "aws_ec2", SourceVars: json.RawMessage(`{"regions":["us-east-1"]}`)},
	}

	// Projects: 1 git (SCM content), 2 manual (no content).
	e.Projects = []fProject{
		{ID: 1, Name: "infra", ScmType: "git", ScmURL: "https://github.com/example/infra.git", ScmBranch: "main"},
		{ID: 2, Name: "local-scripts", ScmType: "", ScmURL: ""},
	}

	// Credentials.
	e.Credentials = []fCredential{
		{ID: 1, Name: "prod-ssh", Kind: "ssh"},
		{ID: 2, Name: "vault-main", Kind: "vault"},
	}

	// Job templates.
	jt10 := fJobTemplate{ID: 10, Name: "Deploy Web", JobType: "run", Playbook: "site.yml",
		Project: 1, Inventory: 2, SurveyEnabled: true}
	jt10.SummaryFields.Credentials = []fCredSummary{{ID: 1, Name: "prod-ssh", Kind: "ssh"}}
	// Seeded FAILED: the awx-template-failing Baseline's positive case, and the state the
	// mirror was blind to before ADR-0128.
	jt10.Status, jt10.LastJobFailed, jt10.LastJobRun = "failed", true, "2026-07-26T03:00:00Z"
	jt10.NextJobRun, jt10.Forks, jt10.Limit, jt10.JobTags, jt10.BecomeEnabled = "2026-07-27T03:00:00Z", 5, "web*", "deploy", true
	jt10.SummaryFields.Organization = fNamed{ID: 1, Name: "Platform"}
	jt10.SummaryFields.Project = fNamed{ID: 1, Name: "infra"}
	jt11 := fJobTemplate{ID: 11, Name: "Gather Facts", JobType: "run", Playbook: "facts.yml",
		Project: 2, Inventory: 1, SurveyEnabled: false}
	jt11.Status, jt11.LastJobFailed = "successful", false
	jt11.SummaryFields.Organization = fNamed{ID: 2, Name: "Legacy"}
	jt11.SummaryFields.Project = fNamed{ID: 2, Name: "local-scripts"}
	e.JobTemplates = []fJobTemplate{jt10, jt11}

	// Orgs + teams: the tenancy/RBAC containers the projection mirrors.
	e.Organizations = []fOrganization{
		{ID: 1, Name: "Platform", Description: "platform engineering"},
		{ID: 2, Name: "Legacy", Description: "inherited estate"},
	}
	e.Teams = []fTeam{{ID: 1, Name: "web-ops"}, {ID: 2, Name: "dba"}}
	e.Teams[0].SummaryFields.Organization = fNamed{ID: 1, Name: "Platform"}
	e.Teams[1].SummaryFields.Organization = fNamed{ID: 2, Name: "Legacy"}

	// Schedules: THREE, so the collection spans pages (pageSize is 2) and the Syncer's
	// paging is exercised on a projection-only endpoint. One targets a workflow rather
	// than a job template (the schedule -> unified_job_template edge picks its target
	// scheme from unified_job_type), and one is DISABLED — the dead-automation case the
	// awx-schedule-enabled Baseline reads.
	sched := func(id int, name, rrule string, enabled bool, ujt int, ujtName, ujtType string) fSchedule {
		s := fSchedule{ID: id, Name: name, RRule: rrule, Enabled: enabled, UnifiedJobTemplate: ujt}
		s.SummaryFields.UnifiedJobTemplate.ID = ujt
		s.SummaryFields.UnifiedJobTemplate.Name = ujtName
		s.SummaryFields.UnifiedJobTemplate.UnifiedJobType = ujtType
		return s
	}
	e.Schedules = []fSchedule{
		sched(30, "nightly-deploy", "DTSTART;FREQ=DAILY;INTERVAL=1", true, 10, "Deploy Web", "job_template"),
		sched(31, "weekly-pipeline", "DTSTART;FREQ=WEEKLY;INTERVAL=1", true, 20, "prod-pipeline", "workflow_job_template"),
		sched(32, "retired-sweep", "DTSTART;FREQ=DAILY;INTERVAL=1", false, 11, "Gather Facts", "job_template"),
	}

	// Survey for JT 10.
	e.Surveys[10] = fSurveySpec{Name: "Deploy Web", Spec: []fSurveyQuestion{
		{Variable: "app_version", Type: "text", QuestionName: "App Version", Required: true, Default: json.RawMessage(`"1.0"`)},
		{Variable: "replicas", Type: "integer", QuestionName: "Replicas", Min: iptr(1), Max: iptr(10), Default: json.RawMessage(`3`)},
		{Variable: "tier", Type: "multiplechoice", QuestionName: "Tier", Required: true, Choices: json.RawMessage(`["gold","silver"]`)},
		{Variable: "api_token", Type: "password", QuestionName: "API Token"},
	}}

	// Workflow with an approval node and a success/failure fan-out.
	e.WorkflowJTs = []fWorkflowJT{{ID: 20, Name: "prod-pipeline", Description: "build, approve, deploy"}}
	e.WorkflowJTs[0].SummaryFields.Organization = fNamed{ID: 1, Name: "Platform"}
	node := func(id int, ident string, ujt int, ujtType string, ok, fail []int, timeout int) fWorkflowNode {
		n := fWorkflowNode{ID: id, Identifier: ident, UnifiedJobTemplate: ujt,
			SuccessNodes: ok, FailureNodes: fail, Timeout: timeout}
		n.SummaryFields.UnifiedJobTemplate.ID = ujt
		n.SummaryFields.UnifiedJobTemplate.UnifiedJobType = ujtType
		return n
	}
	e.WorkflowNodes[20] = []fWorkflowNode{
		node(100, "build", 11, "job", []int{101}, nil, 0),
		node(101, "approve", 0, "workflow_approval", []int{102}, nil, 3600),
		node(102, "deploy", 10, "job", []int{103}, []int{104}, 0),
		node(103, "notify-ok", 11, "job", nil, nil, 0),
		node(104, "rollback", 11, "job", nil, nil, 0),
	}

	return e
}
