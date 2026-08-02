package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ── §2 · "Naming is API. Frozen at v1.0." — enforced, not remembered ─────────────────────────────
//
// The charter bans six terms in CORE-MODEL IDENTIFIERS: `inventory`, `playbook`, `job template`,
// `CI`, `CMDB`, `resource`. Each is a tool-specific rendering or a namespace collision, and each has
// a Stratt name that means something precise instead (AWX inventory → **View**, job template →
// **Step preset**, survey → **input Contract**).
//
// Until this test the freeze was enforced by REVIEW ALONE. There was no check anywhere, so it held
// exactly as well as everyone's memory — and on 2026-08-01 a hand review caught a `kind: "inventory"`
// event field that needed real thought to clear (it is admissible: a plugin-emitted, tool-scoped
// label naming ansible's own artifact, exactly as `kind: "tofu"` and `kind: "kubectl"` are). A rule
// whose enforcement is "somebody notices" is one bad afternoon from being false.
//
// ── WHY THIS SCANS THREE SURFACES AND NOT THE REPO ──────────────────────────────────────────────
//
// The ban is on CORE-MODEL identifiers, and the tempting version of this test — grep the tree — is
// the version that gets switched off in a week. Every one of these words is legitimate somewhere:
//
//   - `playbook` and `inventory` are ANSIBLE'S OWN vocabulary, and the charter explicitly keeps
//     tool content unmodified ("playbooks, HCL, charts remain unmodified content"). The ansible
//     plugin must say them; `buildInventory` is correctly named.
//   - `resource` is Kubernetes' own word — RBAC rules say `resources:`, and the chart is full of it.
//   - `CI` is a substring of "specification", "policies", "decision"; as a word it names the thing
//     this repo's own gate IS.
//
// A check that cries wolf on those gets disabled, and then it protects nothing. So this asserts the
// surfaces where the frozen vocabulary actually LIVES and where a violation would be permanent API:
//
//  1. **OpenAPI path segments** — the public REST surface. `/inventories` would be the migration
//     mistake the charter names, wired into every client.
//  2. **Database identifiers** — table and column names in the shipped migrations. A column is
//     forever in a way a variable is not; the expand/contract rule means renaming one is a release.
//  3. **Facet namespaces** — the schema documents under contracts/facets/. A Facet namespace IS the
//     graph's vocabulary (§2.1), and it appears in every estate that reads it.
//
// Deliberately NOT scanned: Go identifiers, comments, plugin internals, chart templates, docs. Those
// are where the words are legitimate, and a guard that has to be argued with is a guard that loses.

// bannedTerm pairs the frozen word with what §2 says instead, so a failure teaches rather than
// scolds. The replacement is the charter's own mapping.
type bannedTerm struct {
	pattern *regexp.Regexp
	term    string
	instead string
}

func bannedTerms() []bannedTerm {
	// Word-boundary matching, case-insensitive. `CI` as a SUBSTRING appears in half the English
	// language; as an identifier token it is the banned thing.
	b := func(re, term, instead string) bannedTerm {
		return bannedTerm{regexp.MustCompile(`(?i)` + re), term, instead}
	}
	return []bannedTerm{
		b(`(^|[^a-z])inventor(y|ies)([^a-z]|$)`, "inventory", "View (a saved, versioned, CaC-declared graph query — charter §2.1)"),
		b(`(^|[^a-z])playbooks?([^a-z]|$)`, "playbook", "Workflow, or the Actuator's declared content (a playbook is tool content, never a core noun)"),
		b(`(^|[^a-z])job[_\- ]?templates?([^a-z]|$)`, "job template", "Step preset (charter §2's own AWX→Stratt mapping)"),
		b(`(^|[^a-z])cmdb([^a-z]|$)`, "CMDB", "the graph — a rebuildable read-model, never a writable CMDB (§1.2, and a permanent non-goal)"),
		b(`(^|[^a-z])resources?([^a-z]|$)`, "resource", "Entity (§2.1) — `resource` collides with Kubernetes and Terraform and means neither here"),
		b(`(^|[^a-z])ci([^a-z]|$)`, "CI", "the specific noun — Run, Workflow, or Trigger; `CI` names a tool category, not a thing in the model"),
	}
}

// scanIdentifier reports every banned term found in one core-model identifier.
func scanIdentifier(id string) []bannedTerm {
	var hits []bannedTerm
	for _, b := range bannedTerms() {
		if b.pattern.MatchString(id) {
			hits = append(hits, b)
		}
	}
	return hits
}

// TestVocabularyFreeze_OpenAPIPaths guards the public REST surface. A path segment is API in the
// strongest sense: it is in every client, every script and every doc the moment it ships.
func TestVocabularyFreeze_OpenAPIPaths(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	// Top-level path keys: two-space indent, leading slash, trailing colon.
	pathKey := regexp.MustCompile(`(?m)^  (/[A-Za-z0-9/{}._-]*):`)
	found := pathKey.FindAllStringSubmatch(string(spec), -1)
	// A guard that matches nothing PASSES, which is the failure this repo keeps finding. If the
	// spec's shape changes, this must fail loudly rather than quietly protect nothing.
	if len(found) < 10 {
		t.Fatalf("only %d OpenAPI paths matched — the spec's shape changed and this guard is now "+
			"scanning almost nothing, which would pass silently forever", len(found))
	}
	for _, m := range found {
		for _, hit := range scanIdentifier(m[1]) {
			t.Errorf("API path %q contains the frozen term %q (charter §2 — naming is API).\n"+
				"      Use instead: %s", m[1], hit.term, hit.instead)
		}
	}
}

// TestVocabularyFreeze_DatabaseIdentifiers guards table and column names. A column outlives almost
// everything else: the expand/contract rule (UPG-1) makes renaming one a release, not an edit.
func TestVocabularyFreeze_DatabaseIdentifiers(t *testing.T) {
	dir := filepath.Join("..", "graph", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	createTable := regexp.MustCompile(`(?i)create\s+table\s+(?:if\s+not\s+exists\s+)?([a-z0-9_.]+)`)
	addColumn := regexp.MustCompile(`(?i)add\s+column\s+(?:if\s+not\s+exists\s+)?([a-z0-9_]+)`)
	// A column definition inside CREATE TABLE: indented name followed by a type.
	columnDef := regexp.MustCompile(`(?im)^\s+([a-z0-9_]+)\s+(?:text|uuid|jsonb|json|boolean|bool|timestamptz|timestamp|bigint|integer|int|smallint|bytea|numeric)`)

	var scanned int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		body := string(raw)
		for _, re := range []*regexp.Regexp{createTable, addColumn, columnDef} {
			for _, m := range re.FindAllStringSubmatch(body, -1) {
				scanned++
				for _, hit := range scanIdentifier(m[1]) {
					t.Errorf("%s declares the identifier %q, which contains the frozen term %q "+
						"(charter §2). A column name is effectively permanent — the expand/contract "+
						"rule makes renaming it a release.\n      Use instead: %s",
						e.Name(), m[1], hit.term, hit.instead)
				}
			}
		}
	}
	if scanned < 50 {
		t.Fatalf("only %d database identifiers matched across the migrations — the patterns no "+
			"longer fit the DDL and this guard is protecting nothing", scanned)
	}
}

// TestVocabularyFreeze_FacetNamespaces guards the graph's own vocabulary. A Facet namespace is §2.1
// naming: it appears in every estate that reads it and in every Blueprint that routes on it.
func TestVocabularyFreeze_FacetNamespaces(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "contracts", "facets")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read contracts/facets: %v", err)
	}
	var scanned, exempt int
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".schema.json")
		if e.IsDir() || name == e.Name() {
			continue
		}
		// A NAMESPACE ROOTED IN A SHIPPED PLUGIN IS THAT TOOL'S VOCABULARY, not Stratt's.
		//
		// `ansible.playbook` is the first thing this guard found, and it is CORRECT: the
		// ansible-automation Connector mirrors AWX's own objects, and a playbook is one. The
		// charter keeps tool content unmodified ("playbooks, HCL, charts remain unmodified
		// content") and bans these words in CORE-MODEL identifiers — `ansible.*` is a projection
		// of somebody else's world, exactly as `kind: "tofu"` is on the event stream.
		//
		// The exemption is DERIVED from `plugins/<root>` rather than listed. A hand-kept list of
		// tool namespaces is a second copy of the plugin registry, and this repo has paid for a
		// divergent second copy five times. Today exactly one root qualifies (`ansible`); the other
		// fifteen — mgmt, net, os, cert, app, software, identity, access, fileset, … — are Stratt's
		// own and stay guarded. A new plugin earns its namespace by existing; core never does.
		if root, _, ok := strings.Cut(name, "."); ok {
			if st, err := os.Stat(filepath.Join("..", "..", "..", "plugins", root)); err == nil && st.IsDir() {
				exempt++
				continue
			}
		}
		scanned++
		for _, hit := range scanIdentifier(name) {
			t.Errorf("Facet namespace %q contains the frozen term %q (charter §2 / §2.1 — a Facet "+
				"namespace is the graph's vocabulary).\n      Use instead: %s", name, hit.term, hit.instead)
		}
	}
	if scanned < 10 {
		t.Fatalf("only %d facet schemas scanned (%d exempt as tool namespaces) — the layout changed "+
			"and this guard is protecting nothing", scanned, exempt)
	}
	// The exemption must stay NARROW. If it ever swallows most of the corpus, it has stopped being
	// "this tool's own words" and become a hole.
	if exempt > scanned {
		t.Errorf("%d facet namespaces were exempted as tool namespaces and only %d guarded — the "+
			"exemption is now the rule, which is not what §2 says", exempt, scanned)
	}
}

// TestVocabularyFreeze_DetectsAViolation is the falsification. Every guard in this repo that could
// not fail turned out not to be checking anything — an `ansible-doc` exit code, a base64 leak test,
// a grep for a constant — so this one proves its own teeth against each banned term rather than
// trusting that the patterns work.
func TestVocabularyFreeze_DetectsAViolation(t *testing.T) {
	violations := map[string]string{
		"/inventories":         "inventory",
		"/hosts/{id}/playbook": "playbook",
		"job_templates":        "job template",
		"cmdb_sync":            "CMDB",
		"resource_id":          "resource",
		"ci_runs":              "CI",
	}
	for id, want := range violations {
		hits := scanIdentifier(id)
		if len(hits) == 0 {
			t.Errorf("%q must be caught as %q and was not — the pattern is not matching, so the "+
				"guard would pass a real violation", id, want)
			continue
		}
		var got []string
		for _, h := range hits {
			got = append(got, h.term)
		}
		if !strings.Contains(strings.Join(got, ","), want) {
			t.Errorf("%q was caught as %v, expected %q", id, got, want)
		}
	}
	// The exemption is by NAMESPACE ROOT, not by the banned word: a core namespace carrying one is
	// still a violation, and a tool namespace is still that tool's business.
	if hits := scanIdentifier("mgmt.inventory"); len(hits) == 0 {
		t.Error("mgmt.inventory must be caught — a CORE namespace does not get ansible's exemption")
	}

	// …and the words that must NOT trip it, because a guard that cries wolf gets switched off.
	// Each of these is a real identifier shape from this repo or its substrate.
	for _, ok := range []string{
		"/runs/{id}/events", "workflow_run_id", "provenance", "decision_log",
		"specification", "policies", "precision", "entity_presence", "cell_id",
	} {
		if hits := scanIdentifier(ok); len(hits) > 0 {
			t.Errorf("%q is legitimate and must not trip the guard, but matched %q — a check that "+
				"fires on ordinary names is one that gets disabled", ok, hits[0].term)
		}
	}
}
