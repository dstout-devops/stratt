package provision

import "testing"

func names(insts []Instance) map[string]bool {
	m := map[string]bool{}
	for _, i := range insts {
		m[i.Name] = true
	}
	return m
}

func TestInstanceName(t *testing.T) {
	cases := []struct {
		prefix         string
		ordinal, count int
		want           string
	}{
		{"web", 1, 3, "web-01"},
		{"web", 10, 12, "web-10"},
		{"db", 2, 2, "db-02"},
		{"node", 7, 100, "node-007"}, // width follows count
	}
	for _, c := range cases {
		if got := InstanceName(c.prefix, c.ordinal, c.count); got != c.want {
			t.Errorf("InstanceName(%q,%d,%d) = %q, want %q", c.prefix, c.ordinal, c.count, got, c.want)
		}
	}
}

// A brand-new fleet: every instance is a gated build, nothing resolved/paused.
func TestPlanNewFleet(t *testing.T) {
	r, err := Plan([]Intent{{Name: "web-fleet", Spec: ComputeSpec{Count: 3, NamePrefix: "web"}}}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := names(r.ToBuild)
	for _, want := range []string{"web-01", "web-02", "web-03"} {
		if !got[want] {
			t.Errorf("new fleet: %q not surfaced for build (got %v)", want, got)
		}
	}
	if len(r.Resolved) != 0 || len(r.Paused) != 0 {
		t.Errorf("new fleet: expected 0 resolved / 0 paused, got %d/%d", len(r.Resolved), len(r.Paused))
	}
}

// Partial fleet: built instances resolve, the rest surface. This is the
// idempotent-convergence property — re-reconciling never rebuilds web-01.
func TestPlanPartialConverges(t *testing.T) {
	r, err := Plan(
		[]Intent{{Name: "web-fleet", Spec: ComputeSpec{Count: 3, NamePrefix: "web"}}},
		map[string]bool{"web-01": true},
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if names(r.ToBuild)["web-01"] {
		t.Error("web-01 is already built — must NOT be surfaced for build (idempotency)")
	}
	if !names(r.ToBuild)["web-02"] || !names(r.ToBuild)["web-03"] {
		t.Errorf("web-02/web-03 must still surface, got %v", names(r.ToBuild))
	}
	if len(r.Resolved) != 1 || r.Resolved[0].Name != "web-01" {
		t.Errorf("web-01 must resolve, got %v", r.Resolved)
	}

	// Fully built => nothing to build, all resolved (converged).
	full, _ := Plan(
		[]Intent{{Name: "web-fleet", Spec: ComputeSpec{Count: 3, NamePrefix: "web"}}},
		map[string]bool{"web-01": true, "web-02": true, "web-03": true},
		0,
	)
	if len(full.ToBuild) != 0 {
		t.Errorf("converged fleet must build nothing, got %v", names(full.ToBuild))
	}
	if len(full.Resolved) != 3 {
		t.Errorf("converged fleet must resolve all 3, got %d", len(full.Resolved))
	}
}

// Two Intents deriving the same instance name is a COMPILE error (§2.4), never
// a silent last-writer-wins.
func TestPlanExclusiveClaim(t *testing.T) {
	_, err := Plan([]Intent{
		{Name: "web-a", Spec: ComputeSpec{Count: 2, NamePrefix: "web"}},
		{Name: "web-b", Spec: ComputeSpec{Count: 2, NamePrefix: "web"}}, // same prefix -> web-01 collision
	}, nil, 0)
	if err == nil {
		t.Fatal("expected an exclusive-claim error for the web-01 collision across two Intents")
	}
}

// §4.3 max-delta: a shortfall beyond the cap pauses the batch instead of fanning
// out — the guardian's mandatory blast-radius gate (M4).
func TestPlanMaxDeltaPauses(t *testing.T) {
	// 50 missing against the default cap of 25 => pause, surface nothing.
	r, err := Plan([]Intent{{Name: "big", Spec: ComputeSpec{Count: 50, NamePrefix: "n"}}}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ToBuild) != 0 {
		t.Errorf("oversized fan-out must NOT surface individual builds, got %d", len(r.ToBuild))
	}
	if len(r.Paused) != 1 || r.Paused[0].Missing != 50 {
		t.Errorf("expected one batch pause of 50, got %v", r.Paused)
	}

	// A per-Intent spec.maxDelta tightens below the cap: count 10, maxDelta 0.2
	// => limit ceil(2) => 10 missing > 2 => pause.
	tight, _ := Plan([]Intent{{Name: "roll", Spec: ComputeSpec{Count: 10, NamePrefix: "r", MaxDelta: 0.2}}}, nil, 0)
	if len(tight.Paused) != 1 || len(tight.ToBuild) != 0 {
		t.Errorf("spec.maxDelta=0.2 must pause a full-fleet build, got build=%d paused=%d", len(tight.ToBuild), len(tight.Paused))
	}

	// Just at the cap does NOT pause.
	ok, _ := Plan([]Intent{{Name: "atcap", Spec: ComputeSpec{Count: 25, NamePrefix: "c"}}}, nil, 0)
	if len(ok.Paused) != 0 || len(ok.ToBuild) != 25 {
		t.Errorf("exactly-cap fleet must surface all builds, got build=%d paused=%d", len(ok.ToBuild), len(ok.Paused))
	}
}

// TestPlanSingletonsShortfall proves named-singleton mode (ADR-0059 decision 4):
// one desired Entity per Intent, correlated by (kind, name), missing ones surfaced.
func TestPlanSingletonsShortfall(t *testing.T) {
	intents := []SingletonIntent{
		{Name: "web-dmz", Kind: "Intent/Subnet", Spec: SingletonSpec{Requires: []string{"provisioning"}}},
		{Name: "db-tier", Kind: "Intent/Subnet", Spec: SingletonSpec{Requires: []string{"provisioning"}}},
	}
	// web-dmz already built; db-tier is not.
	built := map[string]bool{"Intent/Subnet/web-dmz": true}
	r, err := PlanSingletons(intents, built, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ToBuild) != 1 || r.ToBuild[0].Name != "Intent/Subnet/db-tier" {
		t.Fatalf("expected db-tier to build, got %+v", r.ToBuild)
	}
	if len(r.Resolved) != 1 || r.Resolved[0].Name != "Intent/Subnet/web-dmz" {
		t.Fatalf("expected web-dmz resolved, got %+v", r.Resolved)
	}
}

// TestPlanSingletonsNoCrossKindCollision proves a subnet and a dmz named the same
// do NOT collide — the correlation key namespaces by kind (§2).
func TestPlanSingletonsNoCrossKindCollision(t *testing.T) {
	intents := []SingletonIntent{
		{Name: "edge", Kind: "Intent/Subnet", Spec: SingletonSpec{Requires: []string{"provisioning"}}},
		{Name: "edge", Kind: "Intent/Dmz", Spec: SingletonSpec{Requires: []string{"provisioning"}}},
	}
	r, err := PlanSingletons(intents, nil, 0)
	if err != nil {
		t.Fatalf("distinct kinds must not collide: %v", err)
	}
	if len(r.ToBuild) != 2 {
		t.Fatalf("both distinct-kind singletons should build, got %+v", r.ToBuild)
	}
}

// TestPlanSingletonsBatchPauses proves the §4.3 gate pauses a large batch (e.g. a
// 500-record DNS-zone import) instead of fanning out silently.
func TestPlanSingletonsBatchPauses(t *testing.T) {
	var intents []SingletonIntent
	for i := 0; i < 30; i++ {
		intents = append(intents, SingletonIntent{
			Name: "rec-" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Kind: "Intent/DnsRecord", Spec: SingletonSpec{Requires: []string{"provisioning"}},
		})
	}
	r, err := PlanSingletons(intents, nil, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ToBuild) != 0 || len(r.Paused) != 1 {
		t.Fatalf("a 30-build batch over cap 25 must pause, got toBuild=%d paused=%d", len(r.ToBuild), len(r.Paused))
	}
	if r.Paused[0].Missing != 30 {
		t.Fatalf("paused batch should report 30 missing, got %d", r.Paused[0].Missing)
	}
}

// TestPlanSingletonsExclusiveClaim proves two Intents claiming the same (kind, name)
// is a compile error (§2.4), never a silent tiebreak.
func TestPlanSingletonsExclusiveClaim(t *testing.T) {
	intents := []SingletonIntent{
		{Name: "web-dmz", Kind: "Intent/Subnet", Spec: SingletonSpec{Requires: []string{"provisioning"}}},
		{Name: "web-dmz", Kind: "Intent/Subnet", Spec: SingletonSpec{Requires: []string{"provisioning"}}},
	}
	if _, err := PlanSingletons(intents, nil, 0); err == nil {
		t.Fatal("two Intents claiming the same (kind, name) must be a compile error")
	}
}

// TestDetectPlacementDrift proves the pure drift detection (ADR-0059 S5): a unit whose
// declared subnet is not among its observed subnets drifts; converged, un-placed, and
// un-declared units do not.
func TestDetectPlacementDrift(t *testing.T) {
	declared := map[string]string{
		"web-01": "web-dmz", // observed elsewhere -> drift
		"web-02": "web-dmz", // observed in web-dmz -> converged
		"web-03": "web-dmz", // not observed at all -> no signal
		"db-01":  "db-tier", // observed in db-tier -> converged
	}
	observed := map[string][]string{
		"web-01": {"legacy-net"},
		"web-02": {"web-dmz"},
		"db-01":  {"db-tier"},
		"api-01": {"api-net"}, // observed but not declared -> ignored
	}
	drifts := DetectPlacementDrift(declared, observed)
	if len(drifts) != 1 {
		t.Fatalf("expected exactly 1 drift (web-01), got %d: %+v", len(drifts), drifts)
	}
	d := drifts[0]
	if d.Unit != "web-01" || d.Declared != "web-dmz" || len(d.Observed) != 1 || d.Observed[0] != "legacy-net" {
		t.Fatalf("unexpected drift: %+v", d)
	}
}

// TestDeclaredComputePlacements proves each desired instance inherits its Intent's
// placement, and Intents without placement are skipped.
func TestDeclaredComputePlacements(t *testing.T) {
	intents := []Intent{
		{Name: "web", Spec: ComputeSpec{Count: 2, NamePrefix: "web", Placement: &Placement{Subnet: "web-dmz"}}},
		{Name: "cache", Spec: ComputeSpec{Count: 1, NamePrefix: "cache"}}, // no placement
	}
	got := DeclaredComputePlacements(intents)
	if len(got) != 2 || got["web-01"] != "web-dmz" || got["web-02"] != "web-dmz" {
		t.Fatalf("expected web-01/web-02 -> web-dmz, got %v", got)
	}
}
