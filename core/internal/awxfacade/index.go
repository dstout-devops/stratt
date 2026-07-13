package awxfacade

import "net/http"

// awxVersion is the AWX release the façade emulates. Tooling reads this to gate
// behavior; the import target is frozen at 24.6.1 (ADR-0025).
const awxVersion = "24.6.1"

// index serves GET /api/v2/ — the endpoint dict awxkit builds its client from.
// Only the resources the façade implements are advertised.
func (f *Facade) index(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"ping":          "/api/v2/ping/",
		"config":        "/api/v2/config/",
		"me":            "/api/v2/me/",
		"job_templates": "/api/v2/job_templates/",
		"jobs":          "/api/v2/jobs/",
		"inventories":   "/api/v2/inventories/",
	})
}

// ping serves GET /api/v2/ping/ (unauthenticated). version gates client
// behavior.
func (f *Facade) ping(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ha":              false,
		"version":         awxVersion,
		"active_node":     "stratt",
		"install_uuid":    "00000000-0000-0000-0000-000000000000",
		"instances":       []any{},
		"instance_groups": []any{},
	})
}

// config serves GET /api/v2/config/ (unauthenticated). awxkit login reads
// version here.
func (f *Facade) config(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":         awxVersion,
		"ansible_version": "2.19",
		"eula":            "",
		"license_info":    map[string]any{"license_type": "open", "valid_key": true},
		"time_zone":       "UTC",
	})
}

// me serves GET /api/v2/me/ — a one-user list envelope for the authenticated
// Principal (awxkit validates the token here).
func (f *Facade) me(w http.ResponseWriter, r *http.Request) {
	id, kind, _ := principal(r)
	user := map[string]any{
		"id":               awxID(id),
		"username":         id,
		"type":             "user",
		"is_superuser":     false,
		"external_account": kind,
	}
	writeJSON(w, http.StatusOK, envelope{Count: 1, Results: []any{user}})
}
