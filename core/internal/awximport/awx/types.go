package awx

import "encoding/json"

// page is AWX's list envelope: {count,next,previous,results}. Next is an
// absolute or root-relative URL to the following page, "" at the end.
type page[T any] struct {
	Count    int    `json:"count"`
	Next     string `json:"next"`
	Previous string `json:"previous"`
	Results  []T    `json:"results"`
}

// The decode structs below carry AWX's own field names (§2 vendor-rendering
// latitude); only the fields the importer reads are modeled.

// JobTemplate is one AWX job template (→ a single-Step Workflow).
type JobTemplate struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	JobType       string `json:"job_type"` // run | check
	Playbook      string `json:"playbook"`
	Project       int    `json:"project"`
	Inventory     int    `json:"inventory"`
	SurveyEnabled bool   `json:"survey_enabled"`
	// Credential ids are surfaced via the summary_fields relation.
	SummaryFields struct {
		Credentials []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"credentials"`
	} `json:"summary_fields"`
}

// Project resolves a job template's SCM content ref (repo url + branch).
type Project struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	ScmType   string `json:"scm_type"` // "" (manual) | git | svn | ...
	ScmURL    string `json:"scm_url"`
	ScmBranch string `json:"scm_branch"`
}

// WorkflowJobTemplate is an AWX workflow (→ a multi-Step Workflow).
type WorkflowJobTemplate struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// WorkflowNode is one node in a workflow's graph. Edges are id lists onto
// other nodes; exactly one of the job-template / approval fields is set.
type WorkflowNode struct {
	ID                 int    `json:"id"`
	Identifier         string `json:"identifier"`
	UnifiedJobTemplate int    `json:"unified_job_template"`
	SuccessNodes       []int  `json:"success_nodes"`
	FailureNodes       []int  `json:"failure_nodes"`
	AlwaysNodes        []int  `json:"always_nodes"`
	SummaryFields      struct {
		UnifiedJobTemplate struct {
			ID             int    `json:"id"`
			Name           string `json:"name"`
			UnifiedJobType string `json:"unified_job_type"` // job | workflow_approval | ...
		} `json:"unified_job_template"`
	} `json:"summary_fields"`
	// Approval nodes carry a timeout; identity of approvers is not in the API.
	Timeout int `json:"timeout"`
}

// Inventory is an AWX inventory (→ a View). Kind "" is a normal inventory;
// "smart" is a saved host_filter.
type Inventory struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"` // "" | smart
	HostFilter string `json:"host_filter"`
	TotalHosts int    `json:"total_hosts"`
}

// InventorySource is a dynamic population plugin on an inventory.
type InventorySource struct {
	ID         int             `json:"id"`
	Name       string          `json:"name"`
	Source     string          `json:"source"` // aws_ec2 | vmware | azure_rm | gce | ...
	SourceVars json.RawMessage `json:"source_vars"`
}

// Host is a manually-entered inventory host (the writable-CMDB anti-pattern:
// the importer never re-projects these as Entities, §1.2).
type Host struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// Credential is an AWX credential (→ a CredentialRef). Only name/kind are read;
// material is never imported (§2.5) — AWX returns $encrypted$ placeholders.
type Credential struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // ssh | vault | scm | vmware | aws | ...
}

// SurveySpec is a job template's survey (→ an input Contract). Absent unless
// survey_enabled.
type SurveySpec struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Spec        []SurveyQuestion `json:"spec"`
}

// SurveyQuestion is one survey field.
type SurveyQuestion struct {
	QuestionName        string          `json:"question_name"`
	QuestionDescription string          `json:"question_description"`
	Variable            string          `json:"variable"`
	Type                string          `json:"type"` // text|textarea|password|integer|float|multiplechoice|multiselect
	Required            bool            `json:"required"`
	Default             json.RawMessage `json:"default"`
	Min                 *int            `json:"min"`
	Max                 *int            `json:"max"`
	Choices             json.RawMessage `json:"choices"` // string or []string depending on AWX version
}

// Snapshot is the enumerated AWX estate the transform consumes. Relations are
// keyed by AWX id so the transform can resolve cross-references.
type Snapshot struct {
	JobTemplates     []JobTemplate
	WorkflowJTs      []WorkflowJobTemplate
	WorkflowNodes    map[int][]WorkflowNode // by workflow_job_template id
	Projects         map[int]Project        // by project id
	Inventories      []Inventory
	InventorySources map[int][]InventorySource // by inventory id
	Hosts            map[int][]Host            // by inventory id
	Credentials      map[int]Credential        // by credential id
	Surveys          map[int]SurveySpec        // by job_template id
}
