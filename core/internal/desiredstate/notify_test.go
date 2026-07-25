package desiredstate

import (
	"strings"
	"testing"
)

func TestParseNotifyDecls(t *testing.T) {
	root := t.TempDir()
	writeKind(t, root, "views", "v.yaml", "name: v\nselector:\n  kinds: [vm]\n")
	writeKind(t, root, "notify-sinks", "ops.yaml", `
name: ops-webhook
kind: webhook
principal: notify-svc
credentialRef: ops-hook-cred
config:
  bodyTemplate: '{"text":"{{.subject}}"}'
  params:
    method: POST
`)
	// A second Sink on a kind core has never been taught (ADR-0125 D1): it parses,
	// carrying an envelope core cannot interpret. This is the assertion that would
	// fail if a closed kind list ever came back.
	writeKind(t, root, "notify-sinks", "mail.yaml", `
name: ops-mail
kind: smtp
principal: notify-svc
credentialRef: ops-smtp-cred
config:
  params:
    host: smtp.example.test
    from: stratt@example.test
    to: [ops@example.test]
`)
	writeKind(t, root, "subscriptions", "crit.yaml", `
name: crit-drift
on: [finding.open]
match: event.payload.severity == "critical"
sink: ops-webhook
cooldownSeconds: 60
`)
	parsed, err := ParseDir(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.NotifySinks) != 2 {
		t.Fatalf("sinks: %+v", parsed.NotifySinks)
	}
	byName := map[string]int{}
	for i, s := range parsed.NotifySinks {
		byName[s.Name] = i
	}
	if got := parsed.NotifySinks[byName["ops-webhook"]]; got.CredentialRef != "ops-hook-cred" ||
		got.Config.Params["method"] != "POST" {
		t.Fatalf("webhook sink: %+v", got)
	}
	if got := parsed.NotifySinks[byName["ops-mail"]]; got.Kind != "smtp" ||
		got.Config.Params["host"] != "smtp.example.test" {
		t.Fatalf("an unknown-to-core kind must parse with its driver params intact: %+v", got)
	}
	if len(parsed.Subscriptions) != 1 || parsed.Subscriptions[0].Sink != "ops-webhook" {
		t.Fatalf("subscriptions: %+v", parsed.Subscriptions)
	}
}

func TestValidateNotifyRejects(t *testing.T) {
	seed := func(kindDir, file, content string) string {
		root := t.TempDir()
		writeKind(t, root, "views", "v.yaml", "name: v\nselector:\n  kinds: [vm]\n")
		writeKind(t, root, kindDir, file, content)
		return root
	}

	// A Sink with no credentialRef is rejected — secrets are never inline (§2.5).
	root := seed("notify-sinks", "bad.yaml", "name: x\nkind: webhook\nprincipal: p\n")
	if _, err := ParseDir(root, nil); err == nil || !strings.Contains(err.Error(), "credentialRef") {
		t.Fatalf("want credentialRef error, got %v", err)
	}

	// A Sink with no principal is rejected — delivery credential use is
	// authz-checked (§2.5/§1.6), so a Sink must name whom it delivers as.
	root = seed("notify-sinks", "nopr.yaml", "name: x\nkind: webhook\ncredentialRef: c\n")
	if _, err := ParseDir(root, nil); err == nil || !strings.Contains(err.Error(), "principal") {
		t.Fatalf("want principal error, got %v", err)
	}

	// A Sink with no kind is rejected: the kind names the delivery Action, so an
	// empty one names nothing. This is the ONE thing core still checks about a
	// notify kind — that there is one (ADR-0125 D1).
	root = seed("notify-sinks", "nokind.yaml", "name: x\nprincipal: p\ncredentialRef: c\n")
	if _, err := ParseDir(root, nil); err == nil || !strings.Contains(err.Error(), "kind is required") {
		t.Fatalf("want kind error, got %v", err)
	}

	// An unknown notice kind is a declaration error, never a silent no-op.
	root = seed("subscriptions", "bad.yaml", "name: s\non: [run.exploded]\nsink: k\n")
	if _, err := ParseDir(root, nil); err == nil || !strings.Contains(err.Error(), "unknown notice kind") {
		t.Fatalf("want unknown-kind error, got %v", err)
	}

	// A match that does not compile fails the file at parse (§1.8).
	root = seed("subscriptions", "bad2.yaml", "name: s\non: [run.failed]\nsink: k\nmatch: 'event.payload.'\n")
	if _, err := ParseDir(root, nil); err == nil || !strings.Contains(err.Error(), "match") {
		t.Fatalf("want match compile error, got %v", err)
	}

	// A Subscription with an empty on-set is rejected.
	root = seed("subscriptions", "bad3.yaml", "name: s\nsink: k\n")
	if _, err := ParseDir(root, nil); err == nil || !strings.Contains(err.Error(), "at least one notice kind") {
		t.Fatalf("want empty-on error, got %v", err)
	}
}
