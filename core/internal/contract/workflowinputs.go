package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Workflow input schemas (ADR-0118 D2) — the launch boundary, typed.
//
// A Workflow declares what it takes as a JSON Schema object document. This is deliberate
// and not a Go shape: charter line 34's migration table already maps "survey → input
// Contract with UI hints", line 114 requires Contracts be "JSON Schema documents …
// validated by a standard JSON Schema validator, never language classes", and line 120
// makes Step inputs "generated from JSON Schema (Contracts)" so plugins extend the UI "by
// shipping schemas, not React code". A bespoke struct could not drive the form generator
// or an MCP tool schema without a translator in core.
//
// It is a GIT-DECLARED Contract, NOT a plugin Contract: §1.5's registration and
// hash-pinning apply to the embedded registry above, which is where a plugin's schema is
// pinned and drift-checked. A Workflow's inputs are estate desired state reviewed in Git,
// so they carry no pin and no registry row. That distinction has to stay legible, because
// a seam an operator BELIEVES is hash-verified when it is not is worse than an untyped one
// (§1.8).

// CompileInputSchema checks that a declared Workflow input schema is a usable, closed
// object schema, and returns it compiled. Called at declaration so a malformed interface
// fails in Git review rather than at the first launch.
//
// Three properties are enforced beyond "is valid JSON Schema":
//
//   - It must describe an OBJECT. Launch params are a key/value body; a schema for a string
//     or an array could never validate one, so accepting it would be admitting a
//     declaration nothing can satisfy.
//   - It must CLOSE ITSELF (`additionalProperties: false`). The point of typing this seam
//     is that a typo at launch is rejected rather than silently ignored, and an open schema
//     cannot do that. Requiring it in the file (rather than defaulting it) keeps the closed
//     world visible to a reviewer — the same half-declaration rule applied to a port
//     without an address (ADR-0117 D5a) or facetNamespaces without identitySchemes.
//   - Every `default` must satisfy its own property schema. A default that violates the
//     type it is declared under is a lying declaration, and it would otherwise surface as a
//     confusing launch failure on input nobody supplied.
func CompileInputSchema(workflow string, doc json.RawMessage) (*jsonschema.Schema, error) {
	if len(doc) == 0 {
		return nil, nil // no declared inputs: legal, and means "takes nothing"
	}
	var shape map[string]any
	if err := json.Unmarshal(doc, &shape); err != nil {
		return nil, fmt.Errorf("workflow %s: inputs must be a JSON Schema object: %w", workflow, err)
	}
	if t, ok := shape["type"]; !ok || t != "object" {
		return nil, fmt.Errorf(
			"workflow %s: inputs must declare `type: object` (launch params are a key/value body, so any "+
				"other type describes something no launch could satisfy); got type=%v", workflow, shape["type"])
	}
	if ap, ok := shape["additionalProperties"]; !ok || ap != false {
		return nil, fmt.Errorf(
			"workflow %s: inputs must declare `additionalProperties: false` — typing this seam is what makes a "+
				"typo at launch a rejection instead of a silently ignored key, and an open schema cannot do that "+
				"(§1.8). Declare it explicitly so the closed world is visible in review", workflow)
	}
	sch, err := compileAdHoc("stratt:workflow:"+workflow+"/inputs", doc)
	if err != nil {
		return nil, fmt.Errorf("workflow %s: inputs is not a valid JSON Schema: %w", workflow, err)
	}
	if err := checkDefaults(workflow, shape); err != nil {
		return nil, err
	}
	return sch, nil
}

// compileAdHoc compiles one schema document that is NOT in the pinned registry — an
// estate-declared input schema. Deliberately a separate path from load(): registry
// documents are hash-pinned and drift-blocking (§1.5), these are reviewed in Git.
func compileAdHoc(id string, doc json.RawMessage) (*jsonschema.Schema, error) {
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(id, parsed); err != nil {
		return nil, err
	}
	return c.Compile(id)
}

// checkDefaults validates each property's `default` against that property's own subschema.
//
// Honest limit: a subschema carrying `$ref` is SKIPPED, because resolving it standalone
// would need the whole document's reference context and estate input schemas are flat in
// practice. Such a default is still validated at launch, once defaults are applied to the
// instance — later than ideal, and stated rather than implied.
func checkDefaults(workflow string, shape map[string]any) error {
	props, _ := shape["properties"].(map[string]any)
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic error order
	for _, name := range names {
		sub, ok := props[name].(map[string]any)
		if !ok {
			return fmt.Errorf("workflow %s: inputs.properties.%s must be a schema object", workflow, name)
		}
		def, hasDefault := sub["default"]
		if !hasDefault {
			continue
		}
		if _, hasRef := sub["$ref"]; hasRef {
			continue // see the doc comment: not resolvable standalone
		}
		bare := make(map[string]any, len(sub))
		for k, v := range sub {
			if k != "default" {
				bare[k] = v
			}
		}
		raw, err := json.Marshal(bare)
		if err != nil {
			return fmt.Errorf("workflow %s: inputs.properties.%s: %w", workflow, name, err)
		}
		sch, err := compileAdHoc("stratt:workflow:"+workflow+"/inputs/"+name, raw)
		if err != nil {
			return fmt.Errorf("workflow %s: inputs.properties.%s is not a valid schema: %w", workflow, name, err)
		}
		if err := sch.Validate(def); err != nil {
			return fmt.Errorf(
				"workflow %s: inputs.properties.%s has a default (%v) that violates its own schema: %w — a default "+
					"that cannot satisfy the type it is declared under is a lying declaration",
				workflow, name, def, err)
		}
	}
	return nil
}

// InputNames returns the declared input property names — the set a Step's {{.launch.X}}
// binding must be a member of (ADR-0118 D2). Nil doc ⇒ no declared inputs.
func InputNames(doc json.RawMessage) (map[string]bool, error) {
	if len(doc) == 0 {
		return nil, nil
	}
	var shape struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(doc, &shape); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(shape.Properties))
	for name := range shape.Properties {
		out[name] = true
	}
	return out, nil
}

// RequiredNames returns the input names a Workflow's schema marks `required`, sorted.
//
// Split out from InputNames because the two questions have different callers: a
// {{.launch.X}} binding must name a DECLARED input, while a caller that supplies a whole
// param set — a Blueprint's remediationParams or removeParams — must additionally cover the
// REQUIRED ones. Reading `required` here keeps schema shape knowledge in this package rather
// than letting the loader re-parse the document (§3: schemas are data, read in one place).
//
// Nil doc ⇒ nothing required.
func RequiredNames(doc json.RawMessage) ([]string, error) {
	if len(doc) == 0 {
		return nil, nil
	}
	var shape struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(doc, &shape); err != nil {
		return nil, err
	}
	out := append([]string(nil), shape.Required...)
	sort.Strings(out)
	return out, nil
}

// ResolveLaunchInputs applies a Workflow's declared defaults to the supplied launch params
// and validates the result against its input schema, returning the resolved set that
// {{.launch.x}} bindings then read (ADR-0118 D2/D4).
//
// This is THE chokepoint, and it is deliberately one function rather than a check per
// transport. Four paths launch a Workflow — POST /workflows/{name}/runs, MCP
// start_workflow_run, the event Trigger, and the schedule Trigger — and three of them reach
// Temporal without passing through the HTTP handler. Validating in the handler would leave
// the others bypassing it: one capability, one validation model, one audit stream (§1.6).
// It is called at the floor (RunDAG, which no path can skip) and, for a good error message,
// eagerly at the API door.
//
// Instances are canonicalized through JSON before validating, for two reasons: the
// validator expects JSON-shaped values (a Go `int` is not one), and the resolved map then
// carries JSON types consistently regardless of whether it arrived from a request body, a
// YAML Trigger declaration, or a compiled Baseline.
func ResolveLaunchInputs(workflow string, schema json.RawMessage, supplied map[string]any) (map[string]any, error) {
	if len(schema) == 0 {
		// A Workflow declaring no inputs takes none. Accepting them would mean silently
		// discarding what a caller asked for — the same lie as an ignored unknown key, which
		// is what the closed world exists to prevent. This is only enforceable because
		// change context now rides its own field (ADR-0118 D4): while both shared one bag, a
		// policy-gated Workflow's `environment` was indistinguishable from a stray param.
		if len(supplied) > 0 {
			return nil, fmt.Errorf(
				"workflow %s declares no `inputs`, so it accepts no launch inputs (got %v) — declare the launch "+
					"interface, or stop sending them. If these are facts about the CHANGE (environment, "+
					"changeClass, committers), send them as `context`, which policy Steps read and a Workflow "+
					"does not declare", workflow, sortedKeys(supplied))
		}
		return nil, nil
	}
	sch, err := CompileInputSchema(workflow, schema)
	if err != nil {
		return nil, err
	}
	merged, err := withDefaults(schema, supplied)
	if err != nil {
		return nil, err
	}
	inst, err := canonicalize(merged)
	if err != nil {
		return nil, fmt.Errorf("workflow %s: launch inputs: %w", workflow, err)
	}
	// The schema is closed (CompileInputSchema requires additionalProperties: false), so an
	// unknown key fails HERE rather than being quietly dropped — the property D2 promised and
	// the split finally allows.
	if err := sch.Validate(inst); err != nil {
		return nil, fmt.Errorf("workflow %s: launch inputs violate its declared interface: %w", workflow, err)
	}
	out, _ := inst.(map[string]any)
	return out, nil
}

func withDefaults(schema json.RawMessage, supplied map[string]any) (map[string]any, error) {
	var shape struct {
		Properties map[string]struct {
			Default json.RawMessage `json:"default"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &shape); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(supplied)+len(shape.Properties))
	for k, v := range supplied {
		out[k] = v
	}
	for name, prop := range shape.Properties {
		if len(prop.Default) == 0 {
			continue
		}
		if _, supplied := out[name]; supplied {
			continue
		}
		var def any
		if err := json.Unmarshal(prop.Default, &def); err != nil {
			return nil, fmt.Errorf("inputs.properties.%s.default: %w", name, err)
		}
		out[name] = def
	}
	return out, nil
}

func canonicalize(v map[string]any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(raw))
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
