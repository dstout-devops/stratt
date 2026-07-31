package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

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
		// Label associations ride the object we are ALREADY reading, so membership costs
		// no extra request (ADR-0132 D1 / the ADR-0131 budget).
		Labels labelList `json:"labels"`
		// The EE association rides the object we are ALREADY reading — zero extra
		// requests, the same shape as labels (ADR-0133 D5).
		ExecutionEnvironment named `json:"execution_environment"`
	} `json:"summary_fields"`
}

// labelList is AWX's summary_fields sub-collection envelope: {count, results}.
type labelList struct {
	Results []named `json:"results"`
}

// ExecutionEnvironment is an AWX execution environment — a named pointer to the container
// image a job template runs in (ADR-0133 D1). Projected as a SUPPLY-CHAIN fact: the image
// reference is the reason this earns a namespace, and digestPinned is derived from it.
// The registry credential is read only to know WHETHER one is attached, never its material
// (§2.5).
type ExecutionEnvironment struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Image         string `json:"image"`
	Pull          string `json:"pull"`
	Credential    *int   `json:"credential"`
	SummaryFields struct {
		Credential *named `json:"credential"`
	} `json:"summary_fields"`
}

// NotificationTemplate is an AWX notification template — where AWX sends job outcomes
// (AWX-009). It becomes a Sink on cutover: ADR-0125 made a Sink's `kind` NAME ITS DELIVERY
// ACTION and left core holding no driver list, so `notificationType` maps straight across
// and this mirror is actionable rather than decorative.
//
// ── THE §2.5 DECISION, AND IT IS THE WHOLE DESIGN OF THIS TYPE ───────────────────────────
// An AWX notification_configuration CONTAINS CREDENTIALS, and not only in the fields AWX
// marks secret. AWX returns `$encrypted$` for `token`/`password`, but it returns the rest
// IN THE CLEAR — and for the most common driver the cleartext field IS the credential: a
// Slack or Teams incoming-webhook URL is a bearer secret with the token in its path. So
// "only the fields AWX did not encrypt" is not a safe rule; it is the rule that imports
// working webhook credentials into the graph.
//
// This type therefore has NO FIELD THE VALUES COULD LIVE IN. configKeys decodes the object
// and keeps only its KEY NAMES (see its UnmarshalJSON), so the property is structural
// rather than a habit in the normalizer — there is nothing for a later `json.Marshal` of
// this struct to leak, and nothing a well-meaning "just add the endpoint" edit can reach.
// Same line ADR-0128 D2 drew for credentials (name and kind only) and ADR-0132 D3 for
// schedule extraDataKeys (key names, never values), applied where it bites hardest.
//
// The key NAMES are worth having and are not secret: they say which driver knobs an
// operator configured, which is exactly what has to be re-declared as a Sink.
type NotificationTemplate struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// NotificationType is AWX's driver: slack, webhook, email, pagerduty, twilio, irc,
	// mattermost, grafana, rocketchat. Deliberately NOT enumerated here — this is an
	// observed foreign value, and an enum would turn AWX shipping a new driver into a
	// projection failure rather than a fact we render as-is (§1.2).
	NotificationType string     `json:"notification_type"`
	Configuration    configKeys `json:"notification_configuration"`
	// Messages is AWX's per-event message customization. Read as a presence BOOLEAN and
	// never as content: a custom message body can embed job variables, and the fact worth
	// projecting is "this one has hand-written text you must re-author on cutover".
	Messages      json.RawMessage `json:"messages"`
	SummaryFields struct {
		Organization named `json:"organization"`
	} `json:"summary_fields"`
}

// configKeys holds the KEY NAMES of a JSON object and discards every value at decode time.
//
// The discard happens in UnmarshalJSON on purpose. Decoding into a map and filtering later
// would leave the values reachable for as long as the snapshot lives and one edit away from
// a facet; this way the only thing that ever exists in the struct is the list of names.
type configKeys []string

func (k *configKeys) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		// A null or a non-object is "no keys", not a failed projection: AWX's shape here is
		// driver-defined and a mirror must not die on a driver we have not seen.
		*k = nil
		return nil
	}
	out := make([]string, 0, len(raw))
	for name := range raw {
		out = append(out, name)
	}
	sort.Strings(out) // stable: a facet that reorders between polls reads as a change
	*k = out
	return nil
}

// Label is an AWX label — the operator's own grouping vocabulary (ADR-0132 D1).
type Label struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	SummaryFields struct {
		Organization named `json:"organization"`
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

// WorkflowNode is one node of a workflow's graph. Only the fields the PROJECTION derives
// from are modeled (ADR-0129): what the node targets, and what kind of thing that is. The
// edge lists (success/failure/always), identifiers and timeouts are deliberately absent —
// that is the DAG's shape, which is cutover fidelity and belongs to the adopt path.
type WorkflowNode struct {
	ID                 int `json:"id"`
	UnifiedJobTemplate int `json:"unified_job_template"`
	SummaryFields      struct {
		UnifiedJobTemplate struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			// job | workflow_job | workflow_approval | project_update | inventory_update | system_job
			UnifiedJobType string `json:"unified_job_type"`
		} `json:"unified_job_template"`
	} `json:"summary_fields"`
}

type WorkflowJobTemplate struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	SummaryFields struct {
		Organization named     `json:"organization"`
		Labels       labelList `json:"labels"`
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

	// When it actually runs (ADR-0132 D3) — an rrule with no timezone is
	// under-determined, which is what the mirror recorded until now.
	Timezone string `json:"timezone"`
	NextRun  string `json:"next_run"`
	DTStart  string `json:"dtstart"`
	Until    string `json:"until"`
	// ExtraData is decoded ONLY to take its KEY NAMES. Values never reach the graph
	// (§2.5) — the same line ADR-0128 D4 drew when it refused extra_vars.
	ExtraData map[string]json.RawMessage `json:"extra_data"`

	// Per-schedule launch overrides: two schedules of one template are different things.
	JobType       string `json:"job_type"`
	Limit         string `json:"limit"`
	JobTags       string `json:"job_tags"`
	SkipTags      string `json:"skip_tags"`
	Verbosity     int    `json:"verbosity"`
	DiffMode      bool   `json:"diff_mode"`
	Forks         int    `json:"forks"`
	Timeout       int    `json:"timeout"`
	ScmBranch     string `json:"scm_branch"`
	SummaryFields struct {
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

// User is an AWX LOCAL ACCOUNT (ADR-0130 D1) — not an identity. The IdP is the identity
// SoR and `identity.subject` has a single write-owner (the SCIM projector, §2.1); this is
// AWX's own account table, a different system of record describing overlapping people.
// No password hash, no token, no material (§2.5).
type User struct {
	ID              int    `json:"id"`
	Username        string `json:"username"`
	Email           string `json:"email"`
	IsActive        bool   `json:"is_active"`
	IsSuperuser     bool   `json:"is_superuser"`
	IsSystemAuditor bool   `json:"is_system_auditor"`
}

// Snapshot is one full read of the Controller's automation estate.
type Snapshot struct {
	JobTemplates  []JobTemplate
	Workflows     []WorkflowJobTemplate
	Schedules     []Schedule
	Organizations []Organization
	Teams         []Team
	Credentials   []Credential
	Users         []User
	Labels        []Label
	ExecutionEnvs []ExecutionEnvironment
	Notifications []NotificationTemplate
	// WorkflowNodes by workflow_job_template id (ADR-0129). AWX has no bulk endpoint, so
	// this is an N+1 read: one request per workflow, every poll.
	WorkflowNodes map[int][]WorkflowNode
	// TeamMembers by team id (ADR-0130 D2).
	TeamMembers map[int][]User
	// Partial is set when a DETAIL sub-read failed (ADR-0131 D2). The server streams a
	// declined full-sync boundary for it, so the host upserts what WAS read and runs no
	// tombstone sweep — nothing is retracted on the strength of a read that did not
	// finish. Collection failures still fail the whole Observe and never reach here.
	Partial bool
}

// fillDetail populates the expensive per-object tier, refreshing from AWX at most once
// per DetailInterval and serving the cache in between (ADR-0131 D1). Reports whether the
// tier is COMPLETE: false means at least one sub-read failed and the caller must decline
// the full-sync boundary.
//
// The cache is pruned to the objects this cycle actually enumerated, so a deleted
// workflow's nodes do not linger and get re-asserted forever.
func (c *Client) fillDetail(ctx context.Context, snap *Snapshot) (complete bool) {
	c.detailMu.Lock()
	defer c.detailMu.Unlock()

	complete = true
	stale := c.detailAt.IsZero() || c.now().Sub(c.detailAt) >= c.detailInterval
	if stale {
		nodes := make(map[int][]WorkflowNode, len(snap.Workflows))
		for _, wf := range snap.Workflows {
			got, err := list[WorkflowNode](ctx, c, fmt.Sprintf("/workflow_job_templates/%d/workflow_nodes/", wf.ID))
			if err != nil {
				// Keep whatever this workflow had; the sync degrades rather than dies.
				complete = false
				if prev, ok := c.nodeCache[wf.ID]; ok {
					nodes[wf.ID] = prev
				}
				continue
			}
			nodes[wf.ID] = got
		}
		members := make(map[int][]User, len(snap.Teams))
		for _, tm := range snap.Teams {
			got, err := list[User](ctx, c, fmt.Sprintf("/teams/%d/users/", tm.ID))
			if err != nil {
				complete = false
				if prev, ok := c.memberCache[tm.ID]; ok {
					members[tm.ID] = prev
				}
				continue
			}
			members[tm.ID] = got
		}
		c.nodeCache, c.memberCache = nodes, members
		// Only a CLEAN pass resets the clock: a degraded one must retry on the next poll
		// rather than sit on partial data for a whole interval.
		if complete {
			c.detailAt = c.now()
		}
	}

	snap.WorkflowNodes = make(map[int][]WorkflowNode, len(snap.Workflows))
	for _, wf := range snap.Workflows {
		snap.WorkflowNodes[wf.ID] = c.nodeCache[wf.ID]
	}
	snap.TeamMembers = make(map[int][]User, len(snap.Teams))
	for _, tm := range snap.Teams {
		snap.TeamMembers[tm.ID] = c.memberCache[tm.ID]
	}
	return complete
}

// DetailAge reports how long ago the detail tier last refreshed cleanly — surfaced per
// sync so the poll-cost budget is observable rather than notional (ADR-0131 D3).
func (c *Client) DetailAge() time.Duration {
	c.detailMu.Lock()
	defer c.detailMu.Unlock()
	if c.detailAt.IsZero() {
		return 0
	}
	return c.now().Sub(c.detailAt)
}

// Enumerate reads the estate in TWO TIERS (ADR-0131 D1). The seven COLLECTIONS run every
// poll: they carry the entities themselves, so a single failing one still fails the whole
// Observe (an empty projection is never presented as a successful full sync — the core's
// empty-snapshot guardrail then holds steady, §1.8). The expensive per-object DETAIL tier
// runs on its own cadence and degrades rather than dies: a failed sub-read sets
// Snapshot.Partial, the server declines the full-sync boundary, and the host upserts what
// was read without sweeping. One 404 on the last sub-read no longer discards six good
// collection reads with it.
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
	if snap.Users, err = list[User](ctx, c, "/users/"); err != nil {
		return nil, err
	}
	// A COLLECTION read, not a detail one: O(1) per poll, not O(objects). The label
	// ASSOCIATIONS cost nothing — they ride summary_fields on objects already read.
	if snap.Labels, err = list[Label](ctx, c, "/labels/"); err != nil {
		return nil, err
	}
	if snap.ExecutionEnvs, err = list[ExecutionEnvironment](ctx, c, "/execution_environments/"); err != nil {
		return nil, err
	}
	// A COLLECTION read (AWX-009): O(1) per poll. The ATTACHMENTS — which template notifies
	// through which of these, on started/success/error — are deliberately NOT read, and that
	// is a budget decision rather than an oversight. AWX exposes them only as three
	// sub-resources PER TEMPLATE, so projecting them costs 3×len(JobTemplates) requests on
	// top of the existing detail tier: job templates are the largest collection in every real
	// Controller, which is the "different order of cost" ADR-0131 warns about. Booked as a
	// detail-tier opt-in for an operator who needs the edge, not taken by default.
	if snap.Notifications, err = list[NotificationTemplate](ctx, c, "/notification_templates/"); err != nil {
		return nil, err
	}
	// The DETAIL tier (ADR-0131 D1): the expensive per-object sub-reads, on their own
	// cadence and served from cache in between. A failure here DEGRADES the sync — it
	// does not fail it (D2).
	snap.Partial = !c.fillDetail(ctx, &snap)
	return &snap, nil
}
