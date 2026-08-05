package main

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
)

// ── The accepted set and the advertised set are one value ─────────────────────────────────────
//
// This package had NO test files. That is how the usage banner came to advertise
// `stratt import awx …` long after ADR-0086 D1 retired the verb and ADR-0089 D5 deleted its file:
// an operator ran exactly what the banner told them to run and got exit 2, which is the abstraction
// hiding a failure rather than a mechanism (§1.8) in the first place a new user looks.

// captureUsage runs usage() with stderr redirected, because that is where it writes.
func captureUsage(t *testing.T) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	usage()
	_ = w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestUsageAdvertisesExactlyTheAcceptedVerbs(t *testing.T) {
	banner := captureUsage(t)

	// Every accepted verb is advertised.
	for _, v := range verbs() {
		if !strings.Contains(banner, "stratt "+v.name) {
			t.Errorf("verb %q dispatches but the banner never mentions it", v.name)
		}
	}

	// …and every verb the banner NAMES dispatches. This is the direction that broke: the banner
	// is prose, so it can name anything, including something deleted two ADRs ago.
	advertised := regexp.MustCompile(`stratt ([a-z-]+)`).FindAllStringSubmatch(banner, -1)
	if len(advertised) == 0 {
		t.Fatal("the banner names no verbs at all — this test would pass vacuously")
	}
	for _, m := range advertised {
		if _, ok := lookup(m[1]); !ok {
			t.Errorf("the banner advertises %q, which does not dispatch — running it exits 2", m[1])
		}
	}
}

// A census, so neither assertion above can pass by inspecting an empty table.
func TestTheVerbTableIsNotEmpty(t *testing.T) {
	if got := len(verbs()); got < 5 {
		t.Fatalf("the verb table holds %d entries, want at least the five real ones "+
			"(plan, apply, adopt, bundle, pack) — a shrunken table makes every other assertion vacuous", got)
	}
}

// `import` is retired, twice over, and must not come back by either door.
//
// ADR-0086 D1 retired the one-shot verb; ADR-0089 D5 deleted core/cmd/stratt/import.go and left the
// banner line behind. We never import — the projection is always-on (ADR-0025's own amendment) —
// and `adopt` is the per-object path. A bounded bulk-adopt is booked in ADR-0089 if demand appears.
func TestTheRetiredImportVerbStaysRetired(t *testing.T) {
	if _, ok := lookup("import"); ok {
		t.Error("`import` dispatches again — ADR-0086 D1 retired it and ADR-0089 D5 removed it")
	}
	if banner := captureUsage(t); strings.Contains(banner, "import") {
		t.Error("the banner advertises `import` again — the exact defect this table exists to " +
			"prevent: an operator running what the banner says and getting exit 2")
	}
}
