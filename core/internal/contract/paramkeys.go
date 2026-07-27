package contract

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ValidateParamKeys checks the KEY SET of a param map against a pinned Contract, without
// looking at the values. It exists to close a gap that has now produced the same defect
// three times.
//
// A Workflow Step's params routinely carry templates — `{{.launch.params.region}}` — whose
// VALUES cannot be known until launch. Both param validators therefore skipped the whole
// check when `template.Has(params)` was true, deferring everything. But a param map's
// SHAPE is static even when its values are not: which keys a Step sends is written in Git
// and never changes at runtime. Deferring the shape along with the values meant a Step
// could send a param its Action does not accept, pass the estate load, and fail only when
// an operator stood in front of the gate and approved it.
//
// That is exactly how PRV-1 shipped: `compute-build.yaml` sent `subnet` and
// `availabilityZone` into an input Contract declaring `additionalProperties: false`, so
// every Intent/Compute build failed at launch, for months, in the reference estate.
//
// The rule this restores is the one the surrounding package already applies to Contract
// EXISTENCE: check at the moment the reference is WRITTEN, not the moment it runs (§1.8 —
// a failure a human meets at a gate is a failure admitted at a diff).
//
// Deliberately narrow, because a false positive here blocks an estate from loading:
//
//   - only TOP-LEVEL keys, and only when the schema says `additionalProperties: false`.
//     A schema that accepts extra properties is making a promise this must honour.
//   - `required` is checked only for keys that are ABSENT ENTIRELY. A present-but-templated
//     value satisfies `required`; whether it resolves to something valid is launch's job.
//   - values, types, formats, enums and nested objects stay deferred. This is a shape gate,
//     not a second validator.
func ValidateParamKeys(contractName string, params map[string]any) error {
	if err := ensure(); err != nil {
		return err
	}
	c, ok := lookup(contractName)
	if !ok {
		// Existence is the caller's check to make (it can report a better name); a missing
		// contract is not this function's error to invent.
		return nil
	}
	return checkKeys(contractName, c.contract.Schema, params)
}

// paramSchema is the sliver of JSON Schema this gate reads. `additionalProperties` is
// json.RawMessage because it is legally either a boolean or a schema object, and only the
// literal `false` closes the set.
type paramSchema struct {
	Properties           map[string]json.RawMessage `json:"properties"`
	AdditionalProperties json.RawMessage            `json:"additionalProperties"`
	Required             []string                   `json:"required"`
}

func checkKeys(contractName string, schema []byte, params map[string]any) error {
	if len(schema) == 0 {
		return nil
	}
	var s paramSchema
	if err := json.Unmarshal(schema, &s); err != nil {
		// An unparseable pinned schema is a real problem, but it is the compiler's to
		// report — this gate must not be the thing that fails on it.
		return nil
	}

	// Only a schema that CLOSES its property set can call an unknown key an error.
	closed := strings.TrimSpace(string(s.AdditionalProperties)) == "false"
	if closed && len(s.Properties) > 0 {
		var unknown []string
		for k := range params {
			if _, declared := s.Properties[k]; !declared {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			known := make([]string, 0, len(s.Properties))
			for k := range s.Properties {
				known = append(known, k)
			}
			sort.Strings(known)
			return fmt.Errorf(
				"params %s not accepted by contract %q (which declares additionalProperties:false); it accepts [%s]. "+
					"The values are templated and cannot be checked until launch, but the KEYS are static — "+
					"so this would otherwise fail at the gate rather than here (§1.8)",
				quoteAll(unknown), contractName, strings.Join(known, " "))
		}
	}

	// A required key that is written NOWHERE cannot be supplied by any template.
	var missing []string
	for _, r := range s.Required {
		if _, present := params[r]; !present {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("params missing %s, required by contract %q", quoteAll(missing), contractName)
	}
	return nil
}

func quoteAll(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = `"` + s + `"`
	}
	return strings.Join(q, ", ")
}
