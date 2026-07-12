// Package contract loads, pins, and evaluates the platform's Contract and
// Facet-schema documents (charter §1.5, §2.2): JSON Schema as data,
// validated by a standard validator (santhosh-tekuri/jsonschema, scouted
// RECOMMEND — ADR-0015), never language classes. Schema drift against a
// registered pin is blocking.
package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/dstout-devops/stratt/contracts"
	"github.com/dstout-devops/stratt/core/internal/template"
	"github.com/dstout-devops/stratt/types"
)

type compiled struct {
	contract types.Contract
	schema   *jsonschema.Schema
}

var (
	once     sync.Once
	loadErr  error
	byName   map[string]*compiled
	ordered  []types.Contract
	facetSet map[string]*compiled // facet namespace → schema
)

// load parses, hashes, and compiles every embedded document exactly once.
func load() {
	byName = map[string]*compiled{}
	facetSet = map[string]*compiled{}
	compiler := jsonschema.NewCompiler()

	var paths []string
	_ = fs.WalkDir(contracts.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".schema.json") {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)

	for _, path := range paths {
		raw, err := fs.ReadFile(contracts.FS, path)
		if err != nil {
			loadErr = fmt.Errorf("contract: read %s: %w", path, err)
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			loadErr = fmt.Errorf("contract: parse %s: %w", path, err)
			return
		}
		if err := compiler.AddResource(path, doc); err != nil {
			loadErr = fmt.Errorf("contract: add %s: %w", path, err)
			return
		}
		sch, err := compiler.Compile(path)
		if err != nil {
			loadErr = fmt.Errorf("contract: compile %s: %w", path, err)
			return
		}
		name := strings.TrimSuffix(path, ".schema.json")
		// Version bumps are sibling files: os.kernel.v2.schema.json is
		// version 2 of facets/os.kernel — same name, new pin row (ADR-0015).
		version := 1
		if i := strings.LastIndex(name, ".v"); i > 0 {
			if n, err := strconv.Atoi(name[i+2:]); err == nil && n > 0 {
				name, version = name[:i], n
			}
		}
		sum := sha256.Sum256(raw)
		c := &compiled{
			contract: types.Contract{
				Name:    name,
				Version: version,
				Rung:    types.RungHandWritten,
				Hash:    hex.EncodeToString(sum[:]),
				Schema:  raw,
			},
			schema: sch,
		}
		byName[name] = c
		ordered = append(ordered, c.contract)
		if ns, ok := strings.CutPrefix(name, "facets/"); ok {
			facetSet[ns] = c
		}
	}
}

func ensure() error {
	once.Do(load)
	return loadErr
}

// All returns every embedded Contract (documents + pins), name-ordered.
func All() ([]types.Contract, error) {
	if err := ensure(); err != nil {
		return nil, err
	}
	return ordered, nil
}

// Get returns one Contract by name (e.g. "actuators/script.input").
func Get(name string) (types.Contract, bool, error) {
	if err := ensure(); err != nil {
		return types.Contract{}, false, err
	}
	c, ok := byName[name]
	if !ok {
		return types.Contract{}, false, nil
	}
	return c.contract, true, nil
}

// ValidationError carries the schema violation with JSON-pointer locations —
// diagnosis is never hidden (§1.8).
type ValidationError struct {
	Contract string
	Detail   string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("does not satisfy contract %s: %s", e.Contract, e.Detail)
}

// validate evaluates raw JSON against a compiled schema.
func (c *compiled) validate(raw []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return &ValidationError{Contract: c.contract.Name, Detail: "not valid JSON: " + err.Error()}
	}
	if err := c.schema.Validate(inst); err != nil {
		verr, ok := err.(*jsonschema.ValidationError)
		if !ok {
			return &ValidationError{Contract: c.contract.Name, Detail: err.Error()}
		}
		return &ValidationError{Contract: c.contract.Name, Detail: flatten(verr)}
	}
	return nil
}

// flatten renders the causes as "/json/pointer: message" lines.
func flatten(v *jsonschema.ValidationError) string {
	leaves := v.Causes
	if len(leaves) == 0 {
		leaves = []*jsonschema.ValidationError{v}
	}
	printer := message.NewPrinter(language.English)
	parts := make([]string, 0, len(leaves))
	for _, c := range leaves {
		loc := "/" + strings.Join(c.InstanceLocation, "/")
		parts = append(parts, fmt.Sprintf("%s: %s", loc, c.ErrorKind.LocalizedString(printer)))
	}
	return strings.Join(parts, "; ")
}

// ValidateActuatorParams checks Step params against the Actuator's input
// Contract. Actuators without a registered Contract are refused — an
// uncontracted Step surface must not exist (§2.3).
func ValidateActuatorParams(actuator string, params json.RawMessage) error {
	if err := ensure(); err != nil {
		return err
	}
	c, ok := byName["actuators/"+actuator+".input"]
	if !ok {
		return fmt.Errorf("contract: no input contract for actuator %q", actuator)
	}
	if len(params) == 0 {
		params = []byte(`{}`)
	}
	return c.validate(params)
}

// ResolveActuatorParams binds a launch-time param map's {{.ns.x}} templates
// (ADR-0024), then re-validates the resolved params against the Actuator's
// input Contract and returns the JSON the Actuator receives. This moves a
// template-dependent field's validation from declaration time to launch —
// the resolved value, not the placeholder, is what must satisfy the schema —
// while guaranteeing the Actuator never sees unvalidated params (§1.5, §1.8).
func ResolveActuatorParams(actuator string, params map[string]any, ns template.Namespaces) (json.RawMessage, error) {
	resolved, err := template.SubstituteParams(params, ns)
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(`{}`)
	if resolved != nil {
		if raw, err = json.Marshal(resolved); err != nil {
			return nil, err
		}
	}
	if err := ValidateActuatorParams(actuator, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// ValidateDocument evaluates an instance against a schema document that is
// not embedded — e.g. a DB-pinned rung-2/3 Contract (ADR-0022). The schema
// compiles ad hoc; contractName only labels the error (§1.8 pointer detail).
func ValidateDocument(contractName string, schema, instance json.RawMessage) error {
	compiler := jsonschema.NewCompiler()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return &ValidationError{Contract: contractName, Detail: "schema is not valid JSON: " + err.Error()}
	}
	if err := compiler.AddResource("schema.json", doc); err != nil {
		return &ValidationError{Contract: contractName, Detail: "schema: " + err.Error()}
	}
	sch, err := compiler.Compile("schema.json")
	if err != nil {
		return &ValidationError{Contract: contractName, Detail: "schema does not compile: " + err.Error()}
	}
	c := &compiled{contract: types.Contract{Name: contractName}, schema: sch}
	return c.validate(instance)
}

// ValidateFacet checks a Facet value when its namespace has a pinned schema.
// covered=false means no schema exists for the namespace — allowed by
// design: a Facet schema may exist only when a shipping Contract demands it
// (§1.1); absence is not an error.
func ValidateFacet(namespace string, value json.RawMessage) (covered bool, err error) {
	if err := ensure(); err != nil {
		return false, err
	}
	c, ok := facetSet[namespace]
	if !ok {
		return false, nil
	}
	return true, c.validate(value)
}
