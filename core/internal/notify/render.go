package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"text/template"

	"github.com/dstout-devops/stratt/types"
)

// renderBody produces the request body for a delivery. When the Sink declares
// a bodyTemplate it is rendered (Go text/template) over the Notice; otherwise
// the whole Notice is emitted as JSON. The body is not secret — the url/token
// live only in the injected credential — so it is safe to compose here.
func renderBody(sink types.Sink, n types.Notice) (string, error) {
	if sink.Config.BodyTemplate == "" {
		doc, err := json.Marshal(map[string]any{
			"kind":    n.Kind,
			"subject": n.Subject,
			"at":      n.At,
			"payload": n.Payload,
		})
		if err != nil {
			return "", fmt.Errorf("notify: marshal default body: %w", err)
		}
		return string(doc), nil
	}
	tmpl, err := template.New("body").Option("missingkey=zero").Parse(sink.Config.BodyTemplate)
	if err != nil {
		return "", fmt.Errorf("notify: sink %s: bodyTemplate parse: %w", sink.Name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]any{
		"kind":    n.Kind,
		"subject": n.Subject,
		"at":      n.At,
		"payload": n.Payload,
	}); err != nil {
		return "", fmt.Errorf("notify: sink %s: bodyTemplate render: %w", sink.Name, err)
	}
	return buf.String(), nil
}

// noticeVars is the CEL binding for a Subscription's match predicate — the
// Notice as the `event` variable (event.kind, event.subject, event.payload.X),
// reusing the shared rules engine unchanged.
func noticeVars(n types.Notice) map[string]any {
	return map[string]any{
		"kind":    n.Kind,
		"subject": n.Subject,
		"payload": n.Payload,
	}
}

// kindListed reports whether a Subscription's `on` set includes the kind.
func kindListed(on []string, kind string) bool {
	for _, k := range on {
		if k == kind {
			return true
		}
	}
	return false
}
