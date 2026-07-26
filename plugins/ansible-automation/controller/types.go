package controller

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
	Playbook      string `json:"playbook"` // the playbook path within its project's SCM repo
	Project       int    `json:"project"`
	Inventory     int    `json:"inventory"`
	SurveyEnabled bool   `json:"survey_enabled"`

	// ── Run state (ADR-0128 D1/D3) — CURRENT state, four scalars, never a job-event
	// table (§3). The `instance.state` shape: AWX stays authoritative and the next poll
	// reflects the transition.
	Status      string `json:"status"`
	LastJobRun  string `json:"last_job_run"`
	LastJobFail bool   `json:"last_job_failed"`
	NextJobRun  string `json:"next_job_run"`

	// ── Run knobs (ADR-0128 D1) — what the template will actually DO. Every one of these
	// is a typed field in our own ansible.input.v6, so the mirror of the system we are
	// replacing was thinner than our execution Contract until this landed.
	Forks         int    `json:"forks"`
	Limit         string `json:"limit"`
	JobTags       string `json:"job_tags"`
	SkipTags      string `json:"skip_tags"`
	Timeout       int    `json:"timeout"`
	Verbosity     int    `json:"verbosity"`
	DiffMode      bool   `json:"diff_mode"`
	BecomeEnabled bool   `json:"become_enabled"`
	ScmBranch     string `json:"scm_branch"`

	// NOTE: `extra_vars` is deliberately ABSENT and must stay so (ADR-0128 D4). It may
	// carry secret material, and a projection is exactly where §2.5 says that must not go.

	SummaryFields struct {
		Organization named `json:"organization"`
		// Project is the SCM-backed content source (an AWX "Project"); its NAME is the
		// join key onto the content half's projectID — the cross-source `runs` edge
		// points at `<project.name>/<playbook>`.
		Project named `json:"project"`
		// Credentials the template uses. Projected as an EDGE onto ansible.credential,
		// never as an array of names on the facet (ADR-0128 D2) — "which templates use
		// this credential" is a graph traversal.
		Credentials []named `json:"credentials"`
	} `json:"summary_fields"`
}

// Credential is an AWX credential mirrored NAME AND KIND ONLY (ADR-0128 D2). Material is
// never read — AWX returns $encrypted$ placeholders and we do not read even those (§2.5).
// This is the observed mirror of a foreign object; the CredentialRef Named Kind is what it
// becomes when `stratt adopt` takes authority.
type Credential struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
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
	Credentials   []Credential
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
	if snap.Credentials, err = list[Credential](ctx, c, "/credentials/"); err != nil {
		return nil, err
	}
	return &snap, nil
}
