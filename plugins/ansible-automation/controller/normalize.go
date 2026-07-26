package controller

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Entity kinds + identity schemes this Connector projects. Kind == scheme == facet
// namespace per object type; the identity VALUE is controller-qualified so two
// Controllers never collide (§ cross-source identity). These are the `ansible.*`
// observed-projection Kinds — a foreign automation object mirrored read-only, never
// the frozen Stratt Named Kinds (Workflow/Trigger) those become on cutover.
const (
	KindTemplate = "ansible.template" // an AWX job template
	KindWorkflow = "ansible.workflow" // an AWX workflow job template
	KindSchedule = "ansible.schedule" // an AWX schedule (→ Trigger on cutover)
	KindOrg      = "ansible.org"      // an AWX organization (tenancy)
	KindTeam     = "ansible.team"     // an AWX RBAC team
	// KindCredential mirrors an AWX credential NAME AND KIND ONLY (ADR-0128 D2). It exists
	// so "which templates use this credential" is a graph traversal. NOT a CredentialRef —
	// that is the frozen Named Kind an adopt produces; this is the read-only mirror.
	KindCredential = "ansible.credential"
	// KindUser mirrors an AWX LOCAL ACCOUNT (ADR-0130 D1). Deliberately a different kind
	// from the SCIM projector's `user`: an AWX account and an IdP identity are facts from
	// two systems of record, and this claims neither identity.subject nor identity.name
	// (§2.1 single write-owner). NEVER read by authorization (ADR-0079 INV-3).
	KindUser = "ansible.user"
	// KindLabel is an AWX label — the operator's grouping vocabulary, as an ENTITY rather
	// than a graph label key (ADR-0132 D1). Structural, not stylistic: a plugin's label
	// keys are a STATIC grant allowlist registered per key (ADR-0041 single-owner), so a
	// key only discovered by reading the Controller is ungrantable.
	KindLabel = "ansible.label"
	// KindExecutionEnv mirrors an AWX execution environment — the container IMAGE a job
	// template runs in (ADR-0133 D1). A supply-chain fact, which is why it earns a
	// namespace where AWX instance groups deliberately do not: those are a placement
	// model Stratt already answers with Sites and Cells (D4).
	KindExecutionEnv = "ansible.executionenvironment"

	// schemePlaybook is the ansible-project Syncer's OWNED kind — referenced here only
	// as a cross-source relation TARGET (the `runs` edge), never owned or written by AWX.
	// Module isolation (ADR-0046) forbids importing that plugin's package, so the seam is
	// the contract STRING, not a shared Go symbol (the same way a SchemaId crosses modules).
	// AWX must be granted this as a pointable IdentityScheme (strattd), NOT a FacetNamespace.
	schemePlaybook = "ansible.playbook"
)

// qualify controller-namespaces an AWX object id: "<controllerID>/<id>".
func (c *Client) qualify(id int) string { return fmt.Sprintf("%s/%d", c.ctrlID, id) }

// Normalize maps a full Controller read into `ansible.*` ObservedEntities with the
// edges that make them a graph: a schedule → the object it launches, a team → its
// org, a template/workflow → its org. Read-only projection (§1.2): AWX stays the
// system-of-record and keeps executing; this is the always-on mirror.
func (c *Client) Normalize(snap *Snapshot) ([]*pluginv1.ObservedEntity, error) {
	out := make([]*pluginv1.ObservedEntity, 0, len(snap.JobTemplates)+len(snap.Workflows)+len(snap.Schedules)+len(snap.Organizations)+len(snap.Teams))

	orgRel := func(orgID int) []*pluginv1.ObservedRelation {
		if orgID == 0 {
			return nil
		}
		return []*pluginv1.ObservedRelation{{Type: "owned-by", ToScheme: KindOrg, ToValue: c.qualify(orgID)}}
	}

	for _, jt := range snap.JobTemplates {
		// The three field groups ADR-0128 D1 projects, beside the original five. Every key
		// here is in the PINNED closed schema; `extra_vars` is absent and stays absent (§2.5).
		facet, err := json.Marshal(map[string]any{
			"name": jt.Name, "jobType": jt.JobType, "playbook": jt.Playbook,
			"surveyEnabled": jt.SurveyEnabled, "description": jt.Description,
			// Run state — current, not history (§3).
			"lastRunStatus": jt.Status, "lastRunFailed": jt.LastJobFail,
			"lastRunAt": jt.LastJobRun, "nextRunAt": jt.NextJobRun,
			// Run knobs — what it will actually do.
			"forks": jt.Forks, "limit": jt.Limit, "jobTags": jt.JobTags,
			"skipTags": jt.SkipTags, "timeout": jt.Timeout, "verbosity": jt.Verbosity,
			"diffMode": jt.DiffMode, "becomeEnabled": jt.BecomeEnabled, "scmBranch": jt.ScmBranch,
		})
		if err != nil {
			return nil, err
		}
		rels := append(orgRel(jt.SummaryFields.Organization.ID), runsRel(jt)...)
		rels = append(rels, c.credentialRels(jt)...)
		rels = append(rels, c.labelRels(jt.SummaryFields.Labels)...)
		if ee := jt.SummaryFields.ExecutionEnvironment; ee.ID != 0 {
			rels = append(rels, &pluginv1.ObservedRelation{
				Type: "runs-in", ToScheme: KindExecutionEnv, ToValue: c.qualify(ee.ID),
			})
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         KindTemplate,
			IdentityKeys: map[string]string{KindTemplate: c.qualify(jt.ID)},
			Labels:       labels(jt.Name, jt.SummaryFields.Organization.Name),
			Facets:       map[string][]byte{KindTemplate: facet},
			Relations:    rels,
		})
	}

	for _, cr := range snap.Credentials {
		// Name and kind. Never material (§2.5) — the closed schema is what makes that
		// checkable rather than promised.
		facet, err := json.Marshal(map[string]any{"name": cr.Name, "kind": cr.Kind})
		if err != nil {
			return nil, err
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         KindCredential,
			IdentityKeys: map[string]string{KindCredential: c.qualify(cr.ID)},
			Labels:       labels(cr.Name, ""),
			Facets:       map[string][]byte{KindCredential: facet},
		})
	}

	for _, wf := range snap.Workflows {
		nodes := snap.WorkflowNodes[wf.ID]
		invokes, hasGate := c.workflowTopology(nodes)
		facet, err := json.Marshal(map[string]any{
			"name": wf.Name, "description": wf.Description,
			"nodeCount": len(nodes), "hasApprovalGate": hasGate,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         KindWorkflow,
			IdentityKeys: map[string]string{KindWorkflow: c.qualify(wf.ID)},
			Labels:       labels(wf.Name, wf.SummaryFields.Organization.Name),
			Facets:       map[string][]byte{KindWorkflow: facet},
			Relations: append(append(orgRel(wf.SummaryFields.Organization.ID), invokes...),
				c.labelRels(wf.SummaryFields.Labels)...),
		})
	}

	for _, sc := range snap.Schedules {
		facet, err := json.Marshal(map[string]any{
			"name": sc.Name, "rrule": sc.RRule, "enabled": sc.Enabled,
			// When it runs. An rrule with no timezone is under-determined (ADR-0132 D3).
			"timezone": sc.Timezone, "nextRunAt": sc.NextRun,
			"dtStart": sc.DTStart, "until": sc.Until,
			// KEY NAMES only — values never reach the graph (§2.5).
			"extraDataKeys": extraDataKeys(sc.ExtraData),
			// Per-schedule overrides: two schedules of one template are different things.
			"jobType": sc.JobType, "limit": sc.Limit, "jobTags": sc.JobTags,
			"skipTags": sc.SkipTags, "verbosity": sc.Verbosity, "diffMode": sc.DiffMode,
			"forks": sc.Forks, "timeout": sc.Timeout, "scmBranch": sc.ScmBranch,
		})
		if err != nil {
			return nil, err
		}
		// The edge the graph exists for: a schedule launches its unified job template.
		// The target scheme is the KIND of the launched object (template vs workflow).
		var rels []*pluginv1.ObservedRelation
		if sc.UnifiedJobTemplate != 0 {
			scheme := KindTemplate
			if sc.SummaryFields.UnifiedJobTemplate.UnifiedJobType == "workflow_job_template" {
				scheme = KindWorkflow
			}
			rels = []*pluginv1.ObservedRelation{{Type: "schedules", ToScheme: scheme, ToValue: c.qualify(sc.UnifiedJobTemplate)}}
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         KindSchedule,
			IdentityKeys: map[string]string{KindSchedule: c.qualify(sc.ID)},
			Labels:       labels(sc.Name, ""),
			Facets:       map[string][]byte{KindSchedule: facet},
			Relations:    rels,
		})
	}

	for _, org := range snap.Organizations {
		facet, err := json.Marshal(map[string]any{"name": org.Name, "description": org.Description})
		if err != nil {
			return nil, err
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         KindOrg,
			IdentityKeys: map[string]string{KindOrg: c.qualify(org.ID)},
			Labels:       labels(org.Name, ""),
			Facets:       map[string][]byte{KindOrg: facet},
		})
	}

	for _, u := range snap.Users {
		facet, err := json.Marshal(map[string]any{
			"username": u.Username, "email": u.Email, "isActive": u.IsActive,
			"isSuperuser": u.IsSuperuser, "isSystemAuditor": u.IsSystemAuditor,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         KindUser,
			IdentityKeys: map[string]string{KindUser: c.qualify(u.ID)},
			Labels:       labels(u.Username, ""),
			Facets:       map[string][]byte{KindUser: facet},
		})
	}

	for _, ee := range snap.ExecutionEnvs {
		facet, err := json.Marshal(map[string]any{
			"name": ee.Name, "image": ee.Image,
			"digestPinned":          digestPinned(ee.Image),
			"pullPolicy":            ee.Pull,
			"hasRegistryCredential": ee.Credential != nil || ee.SummaryFields.Credential != nil,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         KindExecutionEnv,
			IdentityKeys: map[string]string{KindExecutionEnv: c.qualify(ee.ID)},
			Labels:       labels(ee.Name, ""),
			Facets:       map[string][]byte{KindExecutionEnv: facet},
		})
	}

	for _, lb := range snap.Labels {
		facet, err := json.Marshal(map[string]any{
			"name": lb.Name, "org": lb.SummaryFields.Organization.Name,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         KindLabel,
			IdentityKeys: map[string]string{KindLabel: c.qualify(lb.ID)},
			Labels:       labels(lb.Name, lb.SummaryFields.Organization.Name),
			Facets:       map[string][]byte{KindLabel: facet},
		})
	}

	for _, tm := range snap.Teams {
		facet, err := json.Marshal(map[string]any{"name": tm.Name})
		if err != nil {
			return nil, err
		}
		var rels []*pluginv1.ObservedRelation
		if tm.SummaryFields.Organization.ID != 0 {
			rels = []*pluginv1.ObservedRelation{{Type: "member-of", ToScheme: KindOrg, ToValue: c.qualify(tm.SummaryFields.Organization.ID)}}
		}
		// Membership: an ESTATE FACT, never an authorization one (ADR-0130 D2). Same-source,
		// so it always resolves. Stratt authz stays Principals + OpenFGA tuples, and INV-3
		// keeps the graph out of the decision path structurally.
		for _, m := range snap.TeamMembers[tm.ID] {
			if m.ID == 0 {
				continue
			}
			rels = append(rels, &pluginv1.ObservedRelation{
				Type: "has-member", ToScheme: KindUser, ToValue: c.qualify(m.ID),
			})
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         KindTeam,
			IdentityKeys: map[string]string{KindTeam: c.qualify(tm.ID)},
			Labels:       labels(tm.Name, tm.SummaryFields.Organization.Name),
			Facets:       map[string][]byte{KindTeam: facet},
			Relations:    rels,
		})
	}

	return out, nil
}

// runsRel is the CROSS-SOURCE edge that unifies AWX's orchestration layer with the raw
// Ansible content the CONTENT half projects: `ansible.template --runs-->
// ansible.playbook`. The target is named BY IDENTITY as `<project.name>/<playbook>` —
// the same `<projectID>/<relpath>` key the content half qualifies playbooks under, so the
// operator aligns STRATT_ANSIBLE_CONTENT_ID with the AAP Project name (the
// statement "this AWX Project IS this Git content root"). It is a SOFT reference: the
// host resolves it against the graph and, if the playbook is not projected by any known
// project, DROPS the edge (no vivify, §1.2) — that dropped edge is exactly the
// orphan-template signal the governance Baseline reads. No edge is emitted when AWX gives
// us neither a project name nor a playbook path (nothing to point at).
func runsRel(jt JobTemplate) []*pluginv1.ObservedRelation {
	proj, pb := jt.SummaryFields.Project.Name, jt.Playbook
	if proj == "" || pb == "" {
		return nil
	}
	return []*pluginv1.ObservedRelation{{Type: "runs", ToScheme: schemePlaybook, ToValue: proj + "/" + pb}}
}

// workflowTopology derives, from a workflow's node graph, the only two things the MIRROR
// needs (ADR-0129): what it invokes, and whether a human has to approve something on the
// way through. The graph's SHAPE — ordering, success/failure/always branching, per-node
// timeouts — is deliberately not derived: that is cutover fidelity, and the adopt path
// reads it from AWX directly.
//
// One edge per DISTINCT target, so a workflow with three nodes running one template says
// so once. The node type decides the target scheme on an EXPLICIT switch, and an unknown
// type draws NO edge: a workflow node can legitimately target a project sync or an
// inventory update — objects we do not project — and guessing would draw a confidently
// wrong edge onto an identity that exists and means something else. A missing edge is
// visible (awx-workflow-covered reads exactly that); a wrong one is not (§1.8).
func (c *Client) workflowTopology(nodes []WorkflowNode) (rels []*pluginv1.ObservedRelation, hasApprovalGate bool) {
	seen := map[string]bool{}
	for _, n := range nodes {
		switch n.SummaryFields.UnifiedJobTemplate.UnifiedJobType {
		case "workflow_approval":
			hasApprovalGate = true
			continue // a pause has no target
		case "job":
			c.addInvokes(&rels, seen, KindTemplate, n.UnifiedJobTemplate)
		case "workflow_job":
			c.addInvokes(&rels, seen, KindWorkflow, n.UnifiedJobTemplate)
		default:
			// project_update / inventory_update / system_job / a future AWX type — nothing
			// we project, so nothing to point at.
		}
	}
	return rels, hasApprovalGate
}

func (c *Client) addInvokes(rels *[]*pluginv1.ObservedRelation, seen map[string]bool, scheme string, id int) {
	if id == 0 {
		return
	}
	val := c.qualify(id)
	key := scheme + "\x00" + val
	if seen[key] {
		return
	}
	seen[key] = true
	*rels = append(*rels, &pluginv1.ObservedRelation{Type: "invokes", ToScheme: scheme, ToValue: val})
}

// digestPinned reports whether an image reference names an IMMUTABLE digest rather than a
// mutable tag (ADR-0133 D2) — charter §7.3's standard, made checkable across an estate AWX
// itself will never mention. A structural parse of a reference string, not a
// reinterpretation: the digest is the part after "@", and only a sha256 form counts.
//
// The "@" must come after the last "/" so a registry path containing one cannot be
// mistaken for a digest, and a tag AND digest together ("img:1.2@sha256:…") is still
// pinned — the digest wins in every container runtime.
func digestPinned(image string) bool {
	at := strings.LastIndex(image, "@")
	if at < 0 || at < strings.LastIndex(image, "/") {
		return false
	}
	return strings.HasPrefix(image[at+1:], "sha256:") && len(image[at+1:]) > len("sha256:")
}

// extraDataKeys returns a schedule's extra_data KEY NAMES, sorted for a stable facet.
// The values are deliberately never touched (§2.5, ADR-0132 D3): this distinguishes two
// schedules of one template and shows what a schedule parameterises, while carrying no
// launch-variable content into the graph. A key like `db_password` is itself the signal —
// secret-shaped material passed as a launch var should be a CredentialRef.
func extraDataKeys(m map[string]json.RawMessage) []string {
	if len(m) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// labelRels emits `--has-label-->` per associated label (ADR-0132 D1). The associations
// ride summary_fields on an object already being read, so this costs no request. An edge
// whose label the Controller did not also report as an object is dropped by the host (no
// vivify) — the honest outcome rather than a stub.
func (c *Client) labelRels(l labelList) []*pluginv1.ObservedRelation {
	var out []*pluginv1.ObservedRelation
	for _, lb := range l.Results {
		if lb.ID == 0 {
			continue
		}
		out = append(out, &pluginv1.ObservedRelation{
			Type: "has-label", ToScheme: KindLabel, ToValue: c.qualify(lb.ID),
		})
	}
	return out
}

// credentialRels emits `ansible.template --uses-credential--> ansible.credential` per
// credential the template uses (ADR-0128 D2). SAME-source, unlike the `runs` edge: both
// ends are owned by the controller half, so the target always resolves and an absence
// means AWX genuinely reported none — there is no orphan signal to read here.
func (c *Client) credentialRels(jt JobTemplate) []*pluginv1.ObservedRelation {
	var out []*pluginv1.ObservedRelation
	for _, cr := range jt.SummaryFields.Credentials {
		if cr.ID == 0 {
			continue
		}
		out = append(out, &pluginv1.ObservedRelation{
			Type: "uses-credential", ToScheme: KindCredential, ToValue: c.qualify(cr.ID),
		})
	}
	return out
}

// labels renders the operator-selectable labels: the object name and (when known)
// its owning org, so a View can group AWX content by name/org.
func labels(name, org string) map[string]string {
	m := map[string]string{}
	if name != "" {
		m["ansible.name"] = name
	}
	if org != "" {
		m["ansible.org"] = org
	}
	return m
}
