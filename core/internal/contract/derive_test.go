package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeriveTofuOutputsSchema(t *testing.T) {
	doc, err := DeriveTofuOutputsSchema(map[string]TofuOutputType{
		"name":    {Type: json.RawMessage(`"string"`)},
		"count":   {Type: json.RawMessage(`"number"`)},
		"enabled": {Type: json.RawMessage(`"bool"`)},
		"secret":  {Type: json.RawMessage(`"string"`), Sensitive: true},
		"hosts":   {Type: json.RawMessage(`["list",["object",{"name":"string","port":"number"}]]`)},
		"tags":    {Type: json.RawMessage(`["map","string"]`)},
		"pair":    {Type: json.RawMessage(`["tuple",["string","number"]]`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatal(err)
	}
	props := parsed["properties"].(map[string]any)
	if props["name"].(map[string]any)["type"] != "string" ||
		props["count"].(map[string]any)["type"] != "number" ||
		props["enabled"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("scalars: %v", props)
	}
	hosts := props["hosts"].(map[string]any)
	items := hosts["items"].(map[string]any)
	if hosts["type"] != "array" || items["properties"].(map[string]any)["port"].(map[string]any)["type"] != "number" {
		t.Fatalf("nested list(object): %v", hosts)
	}
	if props["tags"].(map[string]any)["additionalProperties"].(map[string]any)["type"] != "string" {
		t.Fatalf("map: %v", props["tags"])
	}
	if len(props["pair"].(map[string]any)["prefixItems"].([]any)) != 2 {
		t.Fatalf("tuple: %v", props["pair"])
	}
	if !strings.Contains(props["secret"].(map[string]any)["description"].(string), "sensitive") {
		t.Fatalf("sensitive marker: %v", props["secret"])
	}

	// Determinism: identical input → identical bytes (stable hash axis).
	doc2, _ := DeriveTofuOutputsSchema(map[string]TofuOutputType{
		"tags":    {Type: json.RawMessage(`["map","string"]`)},
		"pair":    {Type: json.RawMessage(`["tuple",["string","number"]]`)},
		"name":    {Type: json.RawMessage(`"string"`)},
		"count":   {Type: json.RawMessage(`"number"`)},
		"enabled": {Type: json.RawMessage(`"bool"`)},
		"secret":  {Type: json.RawMessage(`"string"`), Sensitive: true},
		"hosts":   {Type: json.RawMessage(`["list",["object",{"name":"string","port":"number"}]]`)},
	})
	if string(doc) != string(doc2) {
		t.Fatal("derivation must be deterministic regardless of map order")
	}
}
