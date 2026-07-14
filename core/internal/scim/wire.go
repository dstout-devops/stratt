package scim

import (
	"encoding/json"
	"strconv"
	"strings"
)

// SCIM 2.0 wire structs — the protocol shapes an IdP sends. Hand-rolled (no SCIM
// library): only the attributes Okta/Entra actually push are modeled.

type userWire struct {
	UserName   string          `json:"userName"`
	ExternalID string          `json:"externalId"`
	Active     *bool           `json:"active"`
	Emails     json.RawMessage `json:"emails"`
}

// activeOrDefault: a User is active unless the IdP explicitly says otherwise.
func (u userWire) activeOrDefault() bool {
	if u.Active == nil {
		return true
	}
	return *u.Active
}

type groupWire struct {
	DisplayName string `json:"displayName"`
	ExternalID  string `json:"externalId"`
	Members     []struct {
		Value string `json:"value"`
	} `json:"members"`
}

func (g groupWire) memberIDs() []string {
	out := make([]string, 0, len(g.Members))
	for _, m := range g.Members {
		if m.Value != "" {
			out = append(out, m.Value)
		}
	}
	return out
}

type patchWire struct {
	Operations []patchOp `json:"Operations"`
}

type patchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

// activeChange scans a User PatchOp for an active flip, handling the two shapes
// IdPs send: `{"op":"replace","path":"active","value":false}` and
// `{"op":"replace","value":{"active":false}}`. Entra sends the value as a string
// ("False"); parseBool tolerates both. changed=false means no active op present.
func (p patchWire) activeChange() (active, changed bool) {
	for _, op := range p.Operations {
		if !strings.EqualFold(op.Op, "replace") && !strings.EqualFold(op.Op, "add") {
			continue
		}
		if strings.EqualFold(op.Path, "active") {
			if b, ok := parseBool(op.Value); ok {
				return b, true
			}
		}
		if op.Path == "" {
			var obj struct {
				Active json.RawMessage `json:"active"`
			}
			if err := json.Unmarshal(op.Value, &obj); err == nil && len(obj.Active) > 0 {
				if b, ok := parseBool(obj.Active); ok {
					return b, true
				}
			}
		}
	}
	return false, false
}

// memberValues parses a Group PatchOp value into member ids, tolerating both an
// array (`[{"value":"id"}]`) and a single object (`{"value":"id"}`).
func (op patchOp) memberValues() []string {
	if len(op.Value) == 0 {
		return nil
	}
	var arr []struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(op.Value, &arr); err == nil {
		out := make([]string, 0, len(arr))
		for _, m := range arr {
			if m.Value != "" {
				out = append(out, m.Value)
			}
		}
		return out
	}
	var single struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(op.Value, &single); err == nil && single.Value != "" {
		return []string{single.Value}
	}
	return nil
}

// parseBool accepts a JSON bool or a JSON string ("True"/"false") — Entra sends
// the latter.
func parseBool(raw json.RawMessage) (bool, bool) {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if b, err := strconv.ParseBool(strings.TrimSpace(s)); err == nil {
			return b, true
		}
	}
	return false, false
}

// ── discovery descriptors (Okta/Entra probe these before provisioning) ─────────

func serviceProviderConfig() map[string]any {
	return map[string]any{
		"schemas":               []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"documentationUri":      "",
		"patch":                 map[string]any{"supported": true},
		"bulk":                  map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":                map[string]any{"supported": true, "maxResults": 200},
		"changePassword":        map[string]any{"supported": false},
		"sort":                  map[string]any{"supported": false},
		"etag":                  map[string]any{"supported": false},
		"authenticationSchemes": []any{map[string]any{"type": "oauthbearertoken", "name": "OAuth Bearer Token", "description": "Authentication via the IdP's provisioning bearer token."}},
	}
}

func resourceTypes() map[string]any {
	rt := func(name, schema, endpoint string) map[string]any {
		return map[string]any{
			"schemas":  []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"},
			"id":       name,
			"name":     name,
			"endpoint": endpoint,
			"schema":   schema,
			"meta":     map[string]any{"resourceType": "ResourceType"},
		}
	}
	res := []any{
		rt("User", schemaUser, "/Users"),
		rt("Group", schemaGroup, "/Groups"),
	}
	return listResponse(res)
}

func schemasDoc() map[string]any {
	return listResponse([]any{
		map[string]any{"id": schemaUser, "name": "User"},
		map[string]any{"id": schemaGroup, "name": "Group"},
	})
}
