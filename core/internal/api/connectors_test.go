package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

func connTestStore(t *testing.T) *graph.Store {
	t.Helper()
	dsn := os.Getenv("STRATT_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://stratt:stratt-dev@localhost:5432/stratt?sslmode=disable"
	}
	s, err := graph.Connect(context.Background(), dsn)
	if err != nil {
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}
	return s
}

// TestConnectorReadSurface proves the read-only /connectors surface + the D6 runtime status
// on the detail endpoint (a declared-but-not-running Connector shows WHY, §1.8).
func TestConnectorReadSurface(t *testing.T) {
	store := connTestStore(t)
	ctx := context.Background()
	if err := store.UpsertConnector(ctx, types.Connector{Name: "h-conn", Class: "syncer", Address: "a:9090", PluginIdentity: "p", Source: types.Source{Kind: "p", Name: "h-conn"}}); err != nil {
		t.Fatal(err)
	}
	defer store.DeleteConnector(ctx, "h-conn")

	dialErr := "dial a:9090: unreachable"
	s := &Server{Store: store, PluginStatus: func() map[string]PluginRuntimeStatus {
		return map[string]PluginRuntimeStatus{"connector/h-conn": {Enabled: false, Error: &dialErr}}
	}}

	// list
	rec := httptest.NewRecorder()
	s.ListConnectors(rec, httptest.NewRequest(http.MethodGet, "/connectors", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "h-conn") {
		t.Fatalf("list must return the connector: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// detail + D6 status
	rec = httptest.NewRecorder()
	s.GetConnector(rec, httptest.NewRequest(http.MethodGet, "/connectors/h-conn", nil), "h-conn")
	if rec.Code != http.StatusOK {
		t.Fatalf("get code %d: %s", rec.Code, rec.Body.String())
	}
	var d struct {
		Connector types.Connector `json:"connector"`
		Status    *struct {
			Enabled bool   `json:"enabled"`
			Error   string `json:"error"`
		} `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Connector.Name != "h-conn" || d.Connector.Class != "syncer" {
		t.Fatalf("detail must carry the declaration: %+v", d.Connector)
	}
	if d.Status == nil || d.Status.Enabled || d.Status.Error != dialErr {
		t.Fatalf("detail must attach the D6 runtime status (why it isn't running): %+v", d.Status)
	}

	// 404
	rec = httptest.NewRecorder()
	s.GetConnector(rec, httptest.NewRequest(http.MethodGet, "/connectors/nope", nil), "nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a missing connector must 404, got %d", rec.Code)
	}
}

// TestActuatorReadSurface proves the /actuators surface (no Source; D6 status).
func TestActuatorReadSurface(t *testing.T) {
	store := connTestStore(t)
	ctx := context.Background()
	if err := store.UpsertActuator(ctx, types.Actuator{Name: "h-act", Address: "a:9090", PluginIdentity: "helm", DryRunnable: true, ActionNames: []string{"helm/deploy"}}); err != nil {
		t.Fatal(err)
	}
	defer store.DeleteActuator(ctx, "h-act")

	s := &Server{Store: store, PluginStatus: func() map[string]PluginRuntimeStatus {
		return map[string]PluginRuntimeStatus{"actuator/h-act": {Enabled: true}}
	}}

	rec := httptest.NewRecorder()
	s.GetActuator(rec, httptest.NewRequest(http.MethodGet, "/actuators/h-act", nil), "h-act")
	if rec.Code != http.StatusOK {
		t.Fatalf("get code %d: %s", rec.Code, rec.Body.String())
	}
	var d struct {
		Actuator types.Actuator `json:"actuator"`
		Status   *struct {
			Enabled bool `json:"enabled"`
		} `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Actuator.Name != "h-act" || len(d.Actuator.ActionNames) != 1 {
		t.Fatalf("detail must carry the declaration: %+v", d.Actuator)
	}
	if d.Status == nil || !d.Status.Enabled {
		t.Fatalf("detail must attach the enabled runtime status: %+v", d.Status)
	}
}
