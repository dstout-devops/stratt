package rules

import (
	"strings"
	"testing"
)

func TestCompileAndEval(t *testing.T) {
	p, err := Compile(`event.labels.severity == "critical" && emitter == "hooks"`)
	if err != nil {
		t.Fatal(err)
	}
	match, err := p.Eval("hooks", map[string]any{"labels": map[string]any{"severity": "critical"}})
	if err != nil || !match {
		t.Fatalf("match: %v %v", match, err)
	}
	match, err = p.Eval("hooks", map[string]any{"labels": map[string]any{"severity": "warning"}})
	if err != nil || match {
		t.Fatalf("no-match: %v %v", match, err)
	}
	// Missing key → evaluation error, not a silent false.
	if _, err := p.Eval("hooks", map[string]any{}); err == nil {
		t.Fatal("missing key must surface as an error")
	}

	// Syntax error → compile failure (caught at declaration parse).
	if _, err := Compile(`event.x ==`); err == nil {
		t.Fatal("syntax error must fail compile")
	}
	// Non-bool result → compile failure.
	if _, err := Compile(`event.name`); err == nil || !strings.Contains(err.Error(), "bool") {
		t.Fatalf("non-bool must fail compile: %v", err)
	}
}
