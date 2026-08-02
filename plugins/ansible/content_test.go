package ansible

import (
	"errors"
	"strings"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// TestContentSummary covers ADR-0117 D3's §1.8 half. D3 makes the EE image digest the
// single truth about what content a Run had; a digest nobody can read answers no
// question during descent, so the Run states the content by name and version.
func TestContentSummary(t *testing.T) {
	reader := func(body string) func(string) ([]byte, error) {
		return func(p string) ([]byte, error) {
			if p != eeContentManifestPath {
				t.Fatalf("read %q, want the in-image manifest path %q", p, eeContentManifestPath)
			}
			return []byte(body), nil
		}
	}

	got := contentSummary(reader(`{"collections":[{"name":"community.crypto","version":"2.22.3","declared":true}],"roles":[]}`))
	if !strings.Contains(got, "community.crypto==2.22.3") {
		t.Errorf("must name the collection and its exact version: %q", got)
	}
	if strings.Contains(got, "dependency") {
		t.Errorf("a declared collection must not be labelled a dependency: %q", got)
	}

	// A transitive dependency is recorded too, and marked — the manifest states what the
	// image CONTAINS, not what was requested, so provenance covers what nobody asked for.
	got = contentSummary(reader(`{"collections":[{"name":"community.general","version":"9.0.0","declared":false}],"roles":[]}`))
	if !strings.Contains(got, "community.general==9.0.0 (dependency)") {
		t.Errorf("an undeclared, transitively-installed collection must be marked: %q", got)
	}

	// Roles are first-class here (D3 put them in scope after the first revision omitted them).
	got = contentSummary(reader(`{"collections":[],"roles":[{"name":"geerlingguy.certbot","version":"5.2.0","declared":true}]}`))
	if !strings.Contains(got, "roles: geerlingguy.certbot==5.2.0") {
		t.Errorf("roles must be reported: %q", got)
	}
	if strings.Contains(got, "collections:") {
		t.Errorf("an empty section must be omitted rather than printed empty: %q", got)
	}

	// "none" and "unknown" are DIFFERENT answers. A base EE states none; an EE with no
	// manifest at all (a drop-in ansible-builder image — charter §3 compatibility) cannot
	// state it, and saying so is the honest §1.8 answer rather than implying emptiness.
	if got := contentSummary(reader(`{"collections":[],"roles":[]}`)); !strings.Contains(got, "none") {
		t.Errorf("an EE with an empty manifest must state none: %q", got)
	}
	missing := contentSummary(func(string) ([]byte, error) { return nil, errors.New("no such file") })
	if !strings.Contains(missing, "UNKNOWN") {
		t.Errorf("a missing manifest must report UNKNOWN, never be conflated with none: %q", missing)
	}
	if strings.Contains(missing, "none") {
		t.Errorf("UNKNOWN must not also claim none: %q", missing)
	}
	// A corrupt manifest is reported, not silently treated as absent.
	if got := contentSummary(reader(`{not json`)); !strings.Contains(got, "UNREADABLE") {
		t.Errorf("an undecodable manifest must be reported: %q", got)
	}
}

// TestShim_EmitsContentEveryRun: the statement must be on the Run's event stream, not
// merely available — otherwise it is not part of the descent at all.
func TestShim_EmitsContentEveryRun(t *testing.T) {
	req := Request{Params: withDeclaredSSH(t, ""), Targets: []Target{{Name: "web-01", Address: "10.0.0.1"}}}
	out := runShim(t, req, fakeRunner{rc: 0, lines: []string{
		`{"uuid":"1","counter":1,"event":"runner_on_ok","event_data":{"host":"web-01","res":{"changed":false}}}`,
	}})
	for _, r := range out {
		if ev := r.GetEvent(); ev != nil && ev.GetFields()["kind"] == "ee-content" {
			if ev.GetMessage() == "" {
				t.Fatal("the ee-content event must carry a statement")
			}
			// SCOPE_RUN is what makes the statement REACHABLE (ADR-0121): it says this event
			// describes the whole Run, so a descent surface pins it by reading a spine-owned
			// field instead of matching the word "ee-content" — the `if ansible{}` §1.4
			// forbids, and the reason ADR-0117 (j) was refused as first written. Unstamped,
			// the event is still correct and still one line in fifty thousand.
			if ev.GetScope() != pluginv1.TaskEvent_SCOPE_RUN {
				t.Fatalf("the ee-content event must be SCOPE_RUN or it stays unfindable, got %v", ev.GetScope())
			}
			return
		}
	}
	t.Fatal("every Run must record what ansible content its EE carried (kind=ee-content)")
}
