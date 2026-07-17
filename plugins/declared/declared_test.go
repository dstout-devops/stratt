package declared

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestEnumerateProjectsHosts proves the plugin's content-expertise in isolation
// (no gRPC, no core): the host-list files map to `host` ObservedEntities with a
// lowercased dns.fqdn identity and the operator's labels — exactly what a dev
// linux-fleet View (kinds:[host], labels:{os:linux}) needs to select them.
func TestEnumerateProjectsHosts(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "edge.yaml", `
hosts:
  - fqdn: Web-01.edge.stratt.test
    labels: { os: linux, role: web, tier: edge }
  - fqdn: web-02.edge.stratt.test
    labels: { os: linux, role: web, tier: edge }
`)
	write(t, dir, "db.yaml", `
hosts:
  - fqdn: db-01.edge.stratt.test
    labels: { os: linux, role: db }
`)

	ents, err := enumerate(dir)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(ents) != 3 {
		t.Fatalf("expected 3 hosts, got %d", len(ents))
	}
	byFQDN := map[string]*pluginv1.ObservedEntity{}
	for _, e := range ents {
		if e.GetKind() != "host" {
			t.Errorf("kind = %q, want host", e.GetKind())
		}
		fqdn := e.GetIdentityKeys()["dns.fqdn"]
		if fqdn == "" {
			t.Errorf("entity missing dns.fqdn identity: %+v", e)
		}
		if len(e.GetFacets()) != 0 {
			t.Errorf("declared estate must project NO facet (§1.1), got %v", e.GetFacets())
		}
		byFQDN[fqdn] = e
	}
	// Identity is lowercased (Web-01 → web-01) so it correlates regardless of case.
	web := byFQDN["web-01.edge.stratt.test"]
	if web == nil {
		t.Fatalf("web-01 not projected (case-folded identity); got %v", keys(byFQDN))
	}
	if web.GetLabels()["os"] != "linux" || web.GetLabels()["role"] != "web" {
		t.Errorf("web-01 labels = %v, want os=linux role=web", web.GetLabels())
	}
}

func TestEnumerateRejectsBadInventory(t *testing.T) {
	cases := map[string]string{
		"missing fqdn":  "hosts:\n  - labels: { os: linux }\n",
		"unknown field": "hosts:\n  - fqdn: h1.test\n    kind: server\n", // strict decode
		"blank fqdn":    "hosts:\n  - fqdn: \"  \"\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "bad.yaml", content)
			if _, err := enumerate(dir); err == nil {
				t.Errorf("expected an error for %q, got nil", name)
			}
		})
	}
}

// A host declared twice (even across files) is an identity collision, not a
// silent last-writer-wins (§2.4).
func TestEnumerateRejectsDuplicateIdentity(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.yaml", "hosts:\n  - fqdn: dup.test\n")
	write(t, dir, "b.yaml", "hosts:\n  - fqdn: DUP.test\n") // case-folds to the same identity
	if _, err := enumerate(dir); err == nil {
		t.Fatal("expected a duplicate-identity error across files, got nil")
	}
}

// TestManifestIsProjectionOnly locks the two charter-load-bearing manifest
// choices: it advertises no facet Contract (§1.1) and no tombstone scheme, so a
// host dropped from the file is never silently deleted (ADR-0056 §5).
func TestManifestIsProjectionOnly(t *testing.T) {
	srv := NewServer(Config{Path: t.TempDir()}, discard())
	resp, err := srv.GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	m := resp.GetManifest()
	if m.GetClass() != pluginv1.PluginClass_PLUGIN_CLASS_SYNCER {
		t.Errorf("class = %v, want SYNCER", m.GetClass())
	}
	if len(m.GetContracts()) != 0 {
		t.Errorf("expected NO facet Contracts (§1.1), got %v", m.GetContracts())
	}
	if len(m.GetTombstoneSchemes()) != 0 {
		t.Errorf("expected NO tombstone schemes (never silently delete a host, §5), got %v", m.GetTombstoneSchemes())
	}
}

func keys(m map[string]*pluginv1.ObservedEntity) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
