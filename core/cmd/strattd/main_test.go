package main

import (
	"testing"

	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/types"
)

// TestCheckDevPrincipalSafety proves the SEC-2 structural guard: the trusted-
// header auth bypass refuses to boot alongside a real identity backend or in a
// non-dev environment — a safe posture enforced by boot, not operator memory.
func TestCheckDevPrincipalSafety(t *testing.T) {
	cases := []struct {
		name        string
		dev         bool
		oidcIssuer  string
		environment string
		wantErr     bool
	}{
		{name: "off: nothing to guard", dev: false, oidcIssuer: "https://idp", environment: "production", wantErr: false},
		{name: "dev-only, unscoped env: allowed", dev: true, environment: "", wantErr: false},
		{name: "dev-only, dev env: allowed", dev: true, environment: "dev", wantErr: false},
		{name: "dev + OIDC issuer: refuse", dev: true, oidcIssuer: "https://idp", wantErr: true},
		{name: "dev + production: refuse", dev: true, environment: "production", wantErr: true},
		{name: "dev + prod alias: refuse", dev: true, environment: "Prod", wantErr: true},
		{name: "dev + staging: refuse", dev: true, environment: " staging ", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkDevPrincipalSafety(c.dev, c.oidcIssuer, c.environment)
			if (err != nil) != c.wantErr {
				t.Fatalf("checkDevPrincipalSafety(%v,%q,%q) err=%v, wantErr=%v", c.dev, c.oidcIssuer, c.environment, err, c.wantErr)
			}
		})
	}
}

// TestCACOwnsMappedTeam proves the §2.1 one-owner guard: a team is flagged only
// when it is BOTH a SCIM-mapping target AND has its membership declared in CaC.
func TestCACOwnsMappedTeam(t *testing.T) {
	mapped := map[string]bool{"platform": true, "security": true}
	cases := []struct {
		name string
		cac  []authz.Tuple
		want string
	}{
		{
			name: "conflict: CaC declares a mapped team's member",
			cac:  []authz.Tuple{{User: "principal:bob", Relation: authz.RelationMember, Object: "team:platform"}},
			want: "platform",
		},
		{
			name: "no conflict: CaC member on an UNmapped team",
			cac:  []authz.Tuple{{User: "principal:bob", Relation: authz.RelationMember, Object: "team:ops"}},
			want: "",
		},
		{
			name: "no conflict: CaC grants a role to a mapped team (policy, not membership)",
			cac:  []authz.Tuple{{User: "team:platform#member", Relation: authz.RelationRunner, Object: "view:prod"}},
			want: "",
		},
		{
			name: "no conflict: empty CaC",
			cac:  nil,
			want: "",
		},
	}
	for _, c := range cases {
		if got := cacOwnsMappedTeam(c.cac, mapped); got != c.want {
			t.Errorf("%s: cacOwnsMappedTeam = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestLeaderLeaseName proves Cell-scoped lease naming (ADR-0044): the local Cell
// keeps the legacy name (backward-compat) and named Cells are disjoint.
func TestLeaderLeaseName(t *testing.T) {
	if got := leaderLeaseName(""); got != "strattd-leader" {
		t.Fatalf("empty Cell must keep the legacy lease name, got %q", got)
	}
	if got := leaderLeaseName("local"); got != "strattd-leader" {
		t.Fatalf("local Cell must keep the legacy lease name, got %q", got)
	}
	if got := leaderLeaseName("us-east"); got != "strattd-leader-us-east" {
		t.Fatalf("named Cell must be Cell-scoped, got %q", got)
	}
	if leaderLeaseName("us-east") == leaderLeaseName("eu-west") {
		t.Fatal("distinct Cells must produce distinct lease names")
	}
}

// TestIsAuthzHome proves the authz-home predicate (ADR-0044 slice 4): 'local'
// owns authz only when no named Cells are declared; a named fleet's authz-home
// is exactly the flagged Cell; a 'local' daemon in a named fleet loud-fails.
func TestIsAuthzHome(t *testing.T) {
	cell := func(name string, home bool) types.Cell { return types.Cell{Name: name, AuthzHome: home} }
	fleet := []types.Cell{cell("eu", true), cell("us", false)}

	if h, err := isAuthzHome("local", nil); !h || err != nil {
		t.Fatalf("single-cell 'local' must be authz-home: h=%v err=%v", h, err)
	}
	if h, err := isAuthzHome("eu", fleet); !h || err != nil {
		t.Fatalf("the flagged Cell must be authz-home: h=%v err=%v", h, err)
	}
	if h, err := isAuthzHome("us", fleet); h || err != nil {
		t.Fatalf("a non-flagged Cell must not be authz-home: h=%v err=%v", h, err)
	}
	if _, err := isAuthzHome("local", fleet); err == nil {
		t.Fatal("'local' in a named fleet must loud-fail")
	}
}

// TestReconcileDispatchScope proves the slice-6 boot gate (§2.4 exactly-one-
// answer): the daemon's env-derived NATS scope MUST match its Cell's CaC-
// declared DispatchPrefix, else the hub and its DB-less Site agents would scope
// differently — the daemon refuses to boot rather than serve on subjects the
// agents can't find.
func TestReconcileDispatchScope(t *testing.T) {
	fleet := []types.Cell{
		{Name: "eu", DispatchPrefix: ""},    // declared prefix defaults to the name "eu"
		{Name: "us", DispatchPrefix: "usx"}, // explicit override
	}

	// Effective scope derived the way main does: CellScopeToken(cellID, envOverride).
	effective := func(cellID, envOverride string) string { return types.CellScopeToken(cellID, envOverride) }

	// eu with no env override → effective "eu" == declared "eu": OK.
	if err := reconcileDispatchScope("eu", effective("eu", ""), fleet); err != nil {
		t.Fatalf("matching default scope must reconcile: %v", err)
	}
	// us with env override "usx" → effective "usx" == declared "usx": OK.
	if err := reconcileDispatchScope("us", effective("us", "usx"), fleet); err != nil {
		t.Fatalf("matching override scope must reconcile: %v", err)
	}
	// eu with a stray env override "wrong" → effective "wrong" != declared "eu": loud-fail.
	if err := reconcileDispatchScope("eu", effective("eu", "wrong"), fleet); err == nil {
		t.Fatal("env/CaC scope divergence must loud-fail")
	}
	// us WITHOUT the override env → effective "us" != declared "usx": loud-fail
	// (the operator forgot to set STRATT_CELL_DISPATCH_PREFIX).
	if err := reconcileDispatchScope("us", effective("us", ""), fleet); err == nil {
		t.Fatal("a declared DispatchPrefix the env omits must loud-fail")
	}
	// A Cell absent from the declared fleet has no DispatchPrefix to reconcile.
	if err := reconcileDispatchScope("ap", effective("ap", ""), fleet); err != nil {
		t.Fatalf("an undeclared Cell must not loud-fail: %v", err)
	}
}
