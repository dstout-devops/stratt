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
	Log                *slog.Logger
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
