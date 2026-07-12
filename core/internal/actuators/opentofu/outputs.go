package opentofu

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/contract"
)

// tofuOutput is one entry of `tofu output -json` (value already redacted by
// the driver when sensitive).
type tofuOutput struct {
	Sensitive bool            `json:"sensitive"`
	Type      json.RawMessage `json:"type"`
	Value     json.RawMessage `json:"value"`
}

// interpretOutputs parses the outputs document: the reserved stratt_entities
// output becomes Entity observations (validated against its rung-1 Contract),
// and the whole document derives the Step's rung-2 outputs schema (§2.2).
func interpretOutputs(raw json.RawMessage) ([]actuators.EntityObservation, json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	var outputs map[string]tofuOutput
	if err := json.Unmarshal(raw, &outputs); err != nil {
		return nil, nil, fmt.Errorf("opentofu: decode outputs: %w", err)
	}

	var obs []actuators.EntityObservation
	if ent, ok := outputs["stratt_entities"]; ok {
		if err := contract.ValidateNamed("outputs/stratt_entities", ent.Value); err != nil {
			return nil, nil, err
		}
		var wire []struct {
			Kind         string            `json:"kind"`
			IdentityKeys map[string]string `json:"identityKeys"`
			Labels       map[string]string `json:"labels"`
		}
		if err := json.Unmarshal(ent.Value, &wire); err != nil {
			return nil, nil, fmt.Errorf("opentofu: decode stratt_entities: %w", err)
		}
		for i, w := range wire {
			// stratt.* label keys are reserved (the platform stamps
			// stratt.workspace) — a collision fails visibly instead of
			// being silently overwritten (§2.4 spirit, ADR-0017 F3).
			for k := range w.Labels {
				if strings.HasPrefix(k, "stratt.") {
					return nil, nil, fmt.Errorf("opentofu: stratt_entities[%d]: label %q uses the reserved stratt.* prefix", i, k)
				}
			}
			obs = append(obs, actuators.EntityObservation{
				Kind: w.Kind, IdentityKeys: w.IdentityKeys, Labels: w.Labels,
			})
		}
	}

	doc, err := contract.DeriveTofuOutputsSchema(rawTypes(outputs))
	if err != nil {
		return nil, nil, err
	}
	return obs, doc, nil
}

func rawTypes(outputs map[string]tofuOutput) map[string]contract.TofuOutputType {
	out := make(map[string]contract.TofuOutputType, len(outputs))
	for name, o := range outputs {
		out[name] = contract.TofuOutputType{Type: o.Type, Sensitive: o.Sensitive}
	}
	return out
}
