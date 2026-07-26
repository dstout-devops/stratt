package desiredstate

import "testing"

// A declared View can select by TOPOLOGY (ADR-0059 decision 6, exposed through CaC by
// ADR-0132). types.ViewSelector has carried Relations since ADR-0059 and the declaration
// decoder did not, so an operator could select by kind, label and facet but never by an
// edge — the capability existed and was unreachable from the estate. Found while shipping
// awx-prod-templates, whose entire premise is selecting templates by their `has-label`
// edge.
func TestDeclaredViewSelectsByRelation(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", `name: prod-templates
selector:
  kinds: [ansible.template]
  relations:
    - type: has-label
      targetKind: ansible.label
      targetLabels: {ansible.name: prod}
`)
	decls, err := ParseDir(root, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var sel *Declaration
	for i := range decls.Views {
		if decls.Views[i].Name == "prod-templates" {
			sel = &decls.Views[i]
		}
	}
	if sel == nil {
		t.Fatalf("view not parsed; got %+v", decls.Views)
	}
	if len(sel.Selector.Relations) != 1 {
		t.Fatalf("relation predicate dropped by the decoder: %+v", sel.Selector)
	}
	got := sel.Selector.Relations[0]
	if got.Type != "has-label" || got.TargetKind != "ansible.label" || got.TargetLabels["ansible.name"] != "prod" {
		t.Fatalf("relation predicate decoded wrong: %+v", got)
	}
}

// A relation predicate with no type is a typo, not a match-everything.
func TestDeclaredViewRelationRequiresType(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: bad\nselector:\n  kinds: [ansible.template]\n  relations:\n    - targetKind: ansible.label\n")
	if _, err := ParseDir(root, nil); err == nil {
		t.Fatal("a relation predicate with no type must be rejected, not silently ignored")
	}
}
