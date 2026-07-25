package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"text/template"

	"github.com/dstout-devops/stratt/core/internal/contract"
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

// deliveryFor is THE chokepoint between a Sink and the Action that delivers it
// (ADR-0125 D1), and it is deliberately one function rather than three lines
// inline in deliver(): the kind→Action derivation is the whole decision, and
// inline it would have been unreachable by any test that does not stand up
// Temporal and a Postgres-backed Store.
//
// It answers all of core's driver questions at once, and there are only two:
// which Action delivers this Sink, and do its params satisfy that Action's
// pinned input Contract. Notably absent: any check that the kind is one core
// has heard of.
func deliveryFor(sink types.Sink, body string) (string, json.RawMessage, error) {
	action := types.NotifyActionFor(sink.Kind)
	params, err := deliveryParams(sink, body)
	if err != nil {
		return "", nil, err
	}
	// The DRIVER's own pinned Contract is what types its params — so a
	// `method: DELETE` or a misspelled `from:` is refused here, by name, without
	// core knowing either key exists (§1.5/§1.8). A kind no driver provides has
	// no pinned Contract, and is refused at the same door by Action name.
	if err := contract.ValidateActionInput(action, params); err != nil {
		return "", nil, fmt.Errorf("contract: %w", err)
	}
	return action, params, nil
}

// coreDeliveryKeys are the two input fields core owns on EVERY delivery Action,
// whatever the driver: the rendered body, and the name RunAction mounted the
// Sink's credential under. Every other key belongs to the driver (ADR-0125 D2).
var coreDeliveryKeys = []string{"body", "credentialMount"}

// deliveryParams builds the delivery Action's input: the two core-owned fields,
// plus the Sink's opaque driver params merged in. A params key that shadows a
// core-owned one is REFUSED rather than resolved — a silent winner between
// "core rendered the body" and "the Sink supplied one" is precisely the implicit
// precedence §2.4 forbids, and it would let a declaration quietly replace the
// body the §1.8 descent trail says was delivered.
func deliveryParams(sink types.Sink, body string) (json.RawMessage, error) {
	in := map[string]any{"body": body, "credentialMount": sink.CredentialRef}
	for k, v := range sink.Config.Params {
		if slices.Contains(coreDeliveryKeys, k) {
			return nil, fmt.Errorf("sink %s: config.params may not set %q — core owns it on every delivery", sink.Name, k)
		}
		in[k] = v
	}
	doc, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("notify: sink %s: marshal delivery params: %w", sink.Name, err)
	}
	return doc, nil
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
