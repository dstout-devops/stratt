package content

import "testing"

// ── ANS-009 · a multi-document playbook is still one playbook ─────────────────────────────────
//
// The parity register carried this as 🟠 UNEXAMINED: "playbookPlays unmarshals a single document; a
// `---`-separated multi-doc playbook would project only its first doc. Legal but rare." Examining it
// found the claim exactly right — yaml.Unmarshal into a slice stops at the first document — so the
// projection reported a play count and a host list that were both silently short.
//
// Short, not wrong-looking: the Playbook still appears, with fewer plays and fewer hosts than the
// file has. Nothing surfaces a discrepancy, which is the shape §1.8 cares about most.

const multiDoc = `---
- name: first document
  hosts: webservers
  tasks:
    - ansible.builtin.debug: {msg: one}
---
- name: second document
  hosts: dbservers
  tasks:
    - ansible.builtin.debug: {msg: two}
- name: third play, same document
  hosts: cacheservers
  tasks:
    - ansible.builtin.debug: {msg: three}
`

func TestEveryDocumentOfAPlaybookIsProjected(t *testing.T) {
	hosts, plays, ok := playbookPlays([]byte(multiDoc))
	if !ok {
		t.Fatal("a multi-document playbook must still be recognised as a playbook")
	}
	if plays != 3 {
		t.Errorf("plays = %d, want 3 — a document boundary is not the end of the file", plays)
	}
	want := map[string]bool{"webservers": true, "dbservers": true, "cacheservers": true}
	for _, h := range hosts {
		delete(want, h)
	}
	if len(want) != 0 {
		t.Errorf("hosts %v missed %v — a host pattern past the first document is invisible to the estate", hosts, want)
	}
}

// The single-document case is every playbook that exists today, and must be untouched.
func TestASingleDocumentPlaybookIsUnchanged(t *testing.T) {
	hosts, plays, ok := playbookPlays([]byte(`
- name: only play
  hosts: all
  tasks:
    - ansible.builtin.debug: {msg: hi}
`))
	if !ok || plays != 1 || len(hosts) != 1 || hosts[0] != "all" {
		t.Fatalf("single doc: ok=%v plays=%d hosts=%v", ok, plays, hosts)
	}
}

// A file that is YAML but not a playbook must still be refused — the walk uses `ok` to decide
// whether a .yml file is a playbook at all, so a looser parse would project every vars file.
func TestANonPlaybookYAMLIsStillNotAPlaybook(t *testing.T) {
	for _, src := range []string{
		"key: value\nother: thing\n",              // a mapping, not a sequence of plays
		"- just\n- a\n- list\n",                   // a sequence with no hosts/import_playbook
		"---\nkey: value\n---\nsecond: mapping\n", // multi-doc, still not plays
	} {
		if _, _, ok := playbookPlays([]byte(src)); ok {
			t.Errorf("not a playbook, but was projected as one: %q", src)
		}
	}
}

// A file that stops being YAML partway through must be REFUSED, not truncated at the break.
//
// This case exists because falsification found the previous test could not see it: swallowing the
// decoder's error and stopping still passed everything else, since non-playbook YAML is rejected by
// the play loop rather than by the parse. A malformed file would then project its first document
// and hide the rest — a Playbook that looks complete and is not, which is the exact failure ANS-009
// was about in the first place.
func TestAFileThatStopsBeingYAMLIsRefused(t *testing.T) {
	broken := "- name: looks fine\n  hosts: all\n---\n: : : not yaml at all\n"
	if _, plays, ok := playbookPlays([]byte(broken)); ok {
		t.Fatalf("a malformed playbook was projected with %d play(s) — truncation reads as a "+
			"complete file with fewer plays, which is worse than refusing it", plays)
	}
}
