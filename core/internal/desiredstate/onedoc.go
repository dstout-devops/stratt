package desiredstate

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// nullTag is the YAML tag an empty document's content node carries.
const nullTag = "!!null"

// refuseMultiDocument rejects a declaration file containing more than one YAML document.
//
// ── WHY THIS EXISTS: EVERY KIND SILENTLY DROPPED DOCUMENTS 2..N ─────────────────────────────────
// Every per-kind parser opens a decoder and calls `Decode` exactly ONCE. That reads the first
// document and returns; the rest of the file is never looked at. Not a bug in one parser — the same
// line, repeated in every one of them, so the behaviour was uniform and invisible.
//
// Measured while building demos/region-to-cert: two Views written into one file loaded the first and
// dropped the second, and the ONLY reason anyone noticed is that a Workflow happened to reference the
// missing one. A file whose extra documents nothing references would have vanished without a word —
// a declaration in Git, reviewed and merged, that the daemon does not have. That is precisely the
// silent drop §1.8 refuses, and it is the worst variety: the estate looks complete in review.
//
// ── WHY REFUSE RATHER THAN SUPPORT ──────────────────────────────────────────────────────────────
// Supporting multi-document files is the other legitimate answer, and it was weighed. It is a bigger
// change than it looks: every parse function returns ONE name for the duplicate-detection map, an
// Actuator's `contentDir` resolves against the estate root DERIVED FROM ITS FILE PATH, and diagnostics
// throughout name a file rather than a file+index. None of that is hard; all of it is new surface, for
// an affordance no shipped estate uses — every declaration in this repo is one file.
//
// Refusing costs nothing anyone is doing today and closes the hole completely. The message says which
// door to use, so the reader is not left guessing whether it is unsupported or merely broken.
//
// Someone WILL write a multi-document estate file — it is the Kubernetes idiom and the muscle memory
// is universal — which is the argument for making it loud rather than assuming nobody will try.
//
// ── APPLIED IN parseKind, NOT IN EACH PARSER ────────────────────────────────────────────────────
// One call site, before the per-kind parser runs, so it covers every kind that exists and every kind
// added later. Fifteen copies of this check is how the original defect got fifteen copies.
func refuseMultiDocument(path string, raw []byte) error {
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))

	// The FIRST document is the declaration; a parse error here is left to the per-kind parser,
	// which reports it with the field context this function does not have.
	var first yaml.Node
	if err := dec.Decode(&first); err != nil {
		return nil
	}

	for {
		var next yaml.Node
		err := dec.Decode(&next)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			// Malformed trailing content. Report it rather than swallowing it: the per-kind
			// parser has already stopped reading by this point and would never see it, so
			// staying quiet here restores the exact silence this function exists to end.
			return fmt.Errorf("desiredstate: %s: content after the first YAML document does not parse: %w", path, err)
		}
		// A trailing `---`, with or without comments after it, is PUNCTUATION rather than a
		// declaration: it drops nothing, so refusing it would fail files that are entirely fine.
		//
		// The emptiness is ONE LEVEL DOWN, which two earlier versions of this check got wrong by
		// reasoning about it instead of measuring it. Decoding into a yaml.Node yields a
		// DocumentNode, and an empty document is not an empty DocumentNode — it is a DocumentNode
		// whose single child is a `!!null` scalar. Both wrong guesses (`Kind == 0`, and a null tag
		// on the document itself) inspected the wrapper and never the content.
		if next.Kind == yaml.DocumentNode && len(next.Content) == 1 {
			if inner := next.Content[0]; inner.Tag == nullTag ||
				(inner.Kind == yaml.ScalarNode && strings.TrimSpace(inner.Value) == "") {
				continue
			}
		}
		return fmt.Errorf("desiredstate: %s declares more than one YAML document — "+
			"an estate is ONE declaration per file, and everything after the first `---` was "+
			"previously read by nothing at all (§1.8: a declaration that silently does not exist "+
			"is worse than one that fails). Split it into one file per declaration", path)
	}
}
