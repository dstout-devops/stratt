package awx

import "context"

// The subset of each AWX object this Connector PROJECTS. Read-only: material is
// never fetched (§2.5) and nothing is written back (§1.2). Fields mirror AWX's
// literal /api/v2 attributes — the foreign system's vocabulary, quarantined under
// the `ansible.*` projection (like chef.node.* / vcenter.*).

type JobTemplate struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	JobType       string `json:"job_type"` // run | check
	Playbook      string `json:"playbook"`
	Project       int    `json:"project"`
	Inventory     int    `json:"inventory"`
	SurveyEnabled bool   `json:"survey_enabled"`
	SummaryFields struct {
		Organization named `json:"organization"`
	} `json:"summary_fields"`
}

type WorkflowJobTemplate struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	SummaryFields struct {
		Organization named `json:"organization"`
	} `json:"summary_fields"`
}

// Schedule is an AWX schedule (→ a Stratt Trigger on cutover). rrule is the
// iCal recurrence AWX stores; unified_job_template is the object it launches.
type Schedule struct {
	ID                 int    `json:"id"`
	Name               string `json:"name"`
	RRule              string `json:"rrule"`
	Enabled            bool   `json:"enabled"`
	UnifiedJobTemplate int    `json:"unified_job_template"`
	SummaryFields      struct {
		UnifiedJobTemplate struct {
			ID             int    `json:"id"`
			Name           string `json:"name"`
			UnifiedJobType string `json:"unified_job_type"` // job_template | workflow_job_template
		} `json:"unified_job_template"`
	} `json:"summary_fields"`
}

// Organization is an AWX tenancy container (→ authz scoping on cutover).
type Organization struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Team is an AWX RBAC team (→ a Stratt team / OpenFGA group on cutover).
type Team struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	SummaryFields struct {
		Organization named `json:"organization"`
	} `json:"summary_fields"`
}

// named is the AWX summary_fields shape for a referenced object (id + name).
type named struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Snapshot is one full read of the Controller's automation estate.
type Snapshot struct {
	JobTemplates  []JobTemplate
	Workflows     []WorkflowJobTemplate
	Schedules     []Schedule
	Organizations []Organization
	Teams         []Team
}

// Enumerate performs one full read of every projected collection. A single failing
// collection fails the whole Observe (an empty projection is never presented as a
// successful full-sync — the core's empty-snapshot guardrail then holds steady, §1.8).
func (c *Client) Enumerate(ctx context.Context) (*Snapshot, error) {
	var snap Snapshot
	var err error
	if snap.JobTemplates, err = list[JobTemplate](ctx, c, "/job_templates/"); err != nil {
		return nil, err
	}
	if snap.Workflows, err = list[WorkflowJobTemplate](ctx, c, "/workflow_job_templates/"); err != nil {
		return nil, err
	}
	if snap.Schedules, err = list[Schedule](ctx, c, "/schedules/"); err != nil {
		return nil, err
	}
	if snap.Organizations, err = list[Organization](ctx, c, "/organizations/"); err != nil {
		return nil, err
	}
	if snap.Teams, err = list[Team](ctx, c, "/teams/"); err != nil {
		return nil, err
	}
	return &snap, nil
}
