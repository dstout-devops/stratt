// Package awxfacade is the AWX-24.6.1-compatible /api/v2 REST surface (charter
// §5.6, ADR-0026): a thin, STATELESS translation that presents Stratt objects
// as AWX objects so existing tooling (awxkit, the ansible.controller /
// community.awx collections, terraform-provider-awx, CI scripts) keeps
// launching and polling while pointed at Stratt during a cutover.
//
// Per §1.5 the façade is a compat transport, never load-bearing: the native
// /api/v1 is the sovereign contract, this stores no new truth. AWX nouns
// (inventory, job_template, job, playbook) live ONLY in this wire layer —
// never as Stratt core identifiers/tables (§2, the compat-doc boundary).
//
// Mapping: a Stratt Workflow (single actuation Step) → a job_template; a View →
// an inventory; a Run → a job. AWX integer ids are synthesized statelessly from
// names/uuids (id.go + migration 00014). Launch/cancel are the only writes;
// definitions are read-only (they live in Stratt/Git now).
package awxfacade

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"go.temporal.io/sdk/client"

	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/graph"
)

// Config carries the same substrate handles the API Server already holds —
// the façade constructs nothing new (§1.5).
type Config struct {
	Store              *graph.Store
	Bus                *events.Bus
	Temporal           client.Client
	Authz              authz.Authorizer
	OIDC               *authz.OIDCResolver
	DevPrincipalHeader bool
	// SCIMGate, when set, denies a SCIM-deactivated human at resolve time
	// (ADR-0035) — the compat surface must not be a weaker offboarding path than
	// /api/v1 (§1.6 symmetry). Same seam semantics as api.Server.SCIMGate:
	// humans only; service/agent and unknown-to-SCIM never gated.
	SCIMGate func(ctx context.Context, principalID string) error
	Log      *slog.Logger
}

// Facade is the /api/v2 handler.
type Facade struct {
	cfg Config
}

// New builds the façade's http.Handler. Routes are Go 1.22 method+pattern
// entries with absolute /api/v2 paths (no StripPrefix — awxkit builds its
// client from the index dict, so the handler must see the real paths). ping,
// config, and the index are unauthenticated (AWX contract); everything else
// runs through the AWX auth middleware.
func New(cfg Config) http.Handler {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	f := &Facade{cfg: cfg}
	mux := http.NewServeMux()

	// Unauthenticated discovery endpoints.
	mux.HandleFunc("GET /api/v2/", f.index)
	mux.HandleFunc("GET /api/v2/ping/", f.ping)
	mux.HandleFunc("GET /api/v2/config/", f.config)

	// Authenticated surface.
	mux.HandleFunc("GET /api/v2/me/", f.authed(f.me))
	mux.HandleFunc("GET /api/v2/job_templates/", f.authed(f.listJobTemplates))
	mux.HandleFunc("GET /api/v2/job_templates/{id}/", f.authed(f.getJobTemplate))
	mux.HandleFunc("POST /api/v2/job_templates/{id}/launch/", f.authed(f.launch))
	mux.HandleFunc("GET /api/v2/jobs/", f.authed(f.listJobs))
	mux.HandleFunc("GET /api/v2/jobs/{id}/", f.authed(f.getJob))
	mux.HandleFunc("GET /api/v2/jobs/{id}/stdout/", f.authed(f.jobStdout))
	mux.HandleFunc("GET /api/v2/jobs/{id}/cancel/", f.authed(f.canCancel))
	mux.HandleFunc("POST /api/v2/jobs/{id}/cancel/", f.authed(f.cancel))
	// schedules — READ-ONLY, like every other family here: a schedule is a DECLARATION reconciled
	// from Git, and a POST door would make the compat surface a second write path into desired
	// state (§2.2/§2.3).
	mux.HandleFunc("GET /api/v2/schedules/", f.authed(f.listSchedules))
	mux.HandleFunc("GET /api/v2/schedules/{id}/", f.authed(f.getSchedule))
	// workflow_job_templates — the multi-Step/gated Workflows job_templates deliberately skips.
	// Launch goes through the SAME orchestrate.LaunchWorkflowRun the native door calls; the DAG
	// itself is a declaration, so everything else here is read-only.
	mux.HandleFunc("GET /api/v2/workflow_job_templates/", f.authed(f.listWorkflowJobTemplates))
	mux.HandleFunc("GET /api/v2/workflow_job_templates/{id}/", f.authed(f.getWorkflowJobTemplate))
	mux.HandleFunc("GET /api/v2/workflow_job_templates/{id}/workflow_nodes/", f.authed(f.getWorkflowNodes))
	mux.HandleFunc("POST /api/v2/workflow_job_templates/{id}/launch/", f.authed(f.launchWFJT))
	mux.HandleFunc("GET /api/v2/workflow_jobs/", f.authed(f.listWorkflowJobs))
	mux.HandleFunc("GET /api/v2/workflow_jobs/{id}/", f.authed(f.getWorkflowJob))
	// projects — an Actuator's contentDir IS a project (ADR-0134 D2, one Actuator per project).
	// No POST /update/: a project update means "clone the SCM again", and nothing here clones.
	mux.HandleFunc("GET /api/v2/projects/", f.authed(f.listProjects))
	mux.HandleFunc("GET /api/v2/projects/{id}/", f.authed(f.getProject))
	mux.HandleFunc("GET /api/v2/projects/{id}/playbooks/", f.authed(f.getProjectPlaybooks))
	// credentials — READ-ONLY discovery of brokered POINTERS. Attaching one at launch stays in
	// ignored_fields: a Step's credentialRefs are declared and reviewed in Git (ADR-0009), and a
	// launch-time swap would make the compat surface the one door that skips that review.
	mux.HandleFunc("GET /api/v2/credentials/", f.authed(f.listCredentials))
	mux.HandleFunc("GET /api/v2/credentials/{id}/", f.authed(f.getCredential))
	mux.HandleFunc("GET /api/v2/credential_types/", f.authed(f.listCredentialTypes))
	mux.HandleFunc("GET /api/v2/credential_types/{id}/", f.authed(f.getCredentialType))
	mux.HandleFunc("GET /api/v2/inventories/", f.authed(f.listInventories))
	mux.HandleFunc("GET /api/v2/inventories/{id}/", f.authed(f.getInventory))

	return mux
}

// writeJSON renders v as an AWX JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// awxErr renders an AWX-shaped error (`{"detail": "..."}`).
func awxErr(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}
