package awx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/awximport/awx/awxsim"
)

// newSim starts an awxsim httptest server and returns a client pointed at it.
func newSim(t *testing.T) *Client {
	t.Helper()
	sim := awxsim.New("")
	srv := httptest.NewServer(sim.Handler())
	t.Cleanup(srv.Close)
	sim.SetBase(srv.URL)
	return New(Config{Endpoint: srv.URL, Token: "sim-token", HTTPClient: srv.Client()})
}

func TestEnumeratePagesAllCollections(t *testing.T) {
	c := newSim(t)
	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.JobTemplates) != 2 {
		t.Fatalf("job templates: got %d want 2", len(snap.JobTemplates))
	}
	if len(snap.Inventories) != 3 {
		t.Fatalf("inventories: got %d want 3", len(snap.Inventories))
	}
	if len(snap.Credentials) != 2 {
		t.Fatalf("credentials: got %d want 2", len(snap.Credentials))
	}
	// Sub-resources resolved: the git project, the survey, workflow nodes,
	// the dynamic source, and the static hosts.
	if p, ok := snap.Projects[1]; !ok || p.ScmURL == "" {
		t.Fatalf("project 1 (git) not resolved: %+v", p)
	}
	if s, ok := snap.Surveys[10]; !ok || len(s.Spec) != 4 {
		t.Fatalf("survey for jt 10 not resolved: %+v", s)
	}
	if n := snap.WorkflowNodes[20]; len(n) != 5 {
		t.Fatalf("workflow 20 nodes: got %d want 5", len(n))
	}
	if src := snap.InventorySources[2]; len(src) != 1 || src[0].Source != "aws_ec2" {
		t.Fatalf("inventory 2 sources: %+v", src)
	}
	if h := snap.Hosts[1]; len(h) != 3 {
		t.Fatalf("inventory 1 hosts: got %d want 3", len(h))
	}
}

func TestPaginationFollowsNext(t *testing.T) {
	// pageSize is 2 and there are 3 inventories, so the client must follow one
	// next link. If it did not, only 2 would return.
	c := newSim(t)
	invs, err := list[Inventory](context.Background(), c, "/inventories/")
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 3 {
		t.Fatalf("pagination: got %d inventories want 3 (next not followed?)", len(invs))
	}
}

func TestUnauthorizedIsAnError(t *testing.T) {
	sim := awxsim.New("")
	srv := httptest.NewServer(sim.Handler())
	t.Cleanup(srv.Close)
	sim.SetBase(srv.URL)
	// No token → 401.
	c := New(Config{Endpoint: srv.URL, HTTPClient: srv.Client()})
	if _, err := c.Enumerate(context.Background()); err == nil {
		t.Fatal("expected an error when no token is sent")
	}
}

func TestResolveNextLinkOrigin(t *testing.T) {
	c := New(Config{Endpoint: "https://awx.example.com"})
	got := c.resolve("/api/v2/inventories/?page=2")
	if got != "https://awx.example.com/api/v2/inventories/?page=2" {
		t.Fatalf("resolve root-relative: %s", got)
	}
	if got := c.resolve("/inventories/"); got != "https://awx.example.com/api/v2/inventories/" {
		t.Fatalf("resolve api-relative: %s", got)
	}
	abs := "http://other/api/v2/x/"
	if got := c.resolve(abs); got != abs {
		t.Fatalf("resolve absolute: %s", got)
	}
	_ = http.MethodGet
}
