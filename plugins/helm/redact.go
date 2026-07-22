package helm

import (
	"bytes"
	"errors"
	"io"

	"gopkg.in/yaml.v3"
)

// redactManifests masks the values of every `data:` and `stringData:` entry in any
// rendered `kind: Secret` document, leaving all other manifests (and their
// `# Source:` comments — §1.8 descent readability) intact. The event/plan channel is
// NOT a secret channel (§2.5), and a content-blind core cannot redact, so this is
// the plugin's job (mirrors the opentofu Actuator's plan redaction, ADR-0047 §8).
// Parse errors return the input unchanged (helm renders valid YAML — a cold path);
// we redact by structure, never by a line heuristic, so an odd indentation cannot
// leak a Secret value.
func redactManifests(raw []byte) []byte {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	any := false
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return raw
		}
		if root := docRoot(&doc); root != nil && isSecret(root) {
			redactSecretNode(root)
		}
		if err := enc.Encode(&doc); err != nil {
			return raw
		}
		any = true
	}
	if cerr := enc.Close(); cerr != nil || !any {
		return raw
	}
	return out.Bytes()
}

// docRoot returns the mapping node of a decoded document (or the node itself if it
// is already a mapping), else nil.
func docRoot(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}
	if n.Kind == yaml.MappingNode {
		return n
	}
	return nil
}

// isSecret reports whether a mapping node is a Kubernetes Secret (`kind: Secret`).
func isSecret(root *yaml.Node) bool {
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "kind" && root.Content[i+1].Value == "Secret" {
			return true
		}
	}
	return false
}

// redactSecretNode replaces every value under a Secret's `data`/`stringData`
// mappings with "(redacted)".
func redactSecretNode(root *yaml.Node) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		val := root.Content[i+1]
		if (key == "data" || key == "stringData") && val.Kind == yaml.MappingNode {
			for j := 1; j < len(val.Content); j += 2 {
				v := val.Content[j]
				v.Kind = yaml.ScalarNode
				v.Tag = "!!str"
				v.Style = 0
				v.Value = "(redacted)"
				v.Content = nil
			}
		}
	}
}
