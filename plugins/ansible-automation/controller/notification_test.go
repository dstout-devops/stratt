package controller

import (
	"encoding/json"
	"strings"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// searchable renders the projected entities as the text a leak would actually appear in:
// identity keys, labels, and the DECODED facet documents. Not json.Marshal of the entity —
// protobuf JSON base64-encodes the facet bytes, which hides plaintext from a grep in both
// directions.
func searchable(t *testing.T, ents []*pluginv1.ObservedEntity) string {
	t.Helper()
	var b strings.Builder
	for _, e := range ents {
		b.WriteString(e.GetKind())
		for k, v := range e.GetIdentityKeys() {
			b.WriteString(" " + k + "=" + v)
		}
		for k, v := range e.GetLabels() {
			b.WriteString(" " + k + "=" + v)
		}
		for ns, doc := range e.GetFacets() {
			b.WriteString(" " + ns + "=" + string(doc))
		}
		for _, r := range e.GetRelations() {
			b.WriteString(" " + r.GetType() + "->" + r.GetToScheme() + ":" + r.GetToValue())
		}
	}
	return b.String()
}

// THE TEST THAT MAKES §2.5 FALSIFIABLE HERE. It renders the ENTIRE projected entity set as
// searchable text and greps it for secret material of the shape AWX really returns — a
// Slack webhook URL with its token in the path, and the `$encrypted$` placeholder itself.
//
// Those seeds are the point. AWX encrypts `token` and `password` and returns everything
// else IN THE CLEAR, so "project the fields AWX did not encrypt" reads like a safe rule and
// is not: for the commonest driver the cleartext field IS the credential. A fixture with a
// harmless {"channel":"#ops"} would let a value-leaking projection pass.
func TestNotificationProjectionLeaksNoConfigurationValue(t *testing.T) {
	c := &Client{ctrlID: "ctrl-a"}
	snap := &Snapshot{Notifications: []NotificationTemplate{
		{
			ID: 90, Name: "slack-ops", NotificationType: "slack",
			Configuration: configKeys{}, // populated below through the real decoder
		},
	}}
	// Decode through the REAL path rather than hand-filling the struct: the discard happens
	// in UnmarshalJSON, so a test that set the field directly would prove nothing about it.
	raw := []byte(`{"id":90,"name":"slack-ops","notification_type":"slack",
		"notification_configuration":{
			"hook_url":"https://hooks.slack.invalid/services/T0/B0/XXXXSECRETXXXX",
			"channels":["#ops"],"token":"$encrypted$"},
		"messages":{"error":{"body":"{{ job.name }} failed"}}}`)
	var nt NotificationTemplate
	if err := json.Unmarshal(raw, &nt); err != nil {
		t.Fatal(err)
	}
	snap.Notifications = []NotificationTemplate{nt}

	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatal(err)
	}
	blob := searchable(t, ents)
	// NOTE ON THE SEARCH SURFACE, because the first version of this test was VACUOUS: a
	// plain json.Marshal of the entities base64-encodes the facet bytes, so it matched none
	// of the key names — and would equally have matched none of the SECRETS. A leak test
	// that greps an encoding is a test that cannot fail. searchable decodes the facets.
	for _, secret := range []string{"XXXXSECRETXXXX", "hooks.slack.invalid", "#ops", "$encrypted$", "job.name"} {
		if strings.Contains(blob, secret) {
			t.Errorf("the projection carries %q — a notification_configuration VALUE reached the "+
				"graph. AWX leaves non-secret fields in the clear and the cleartext field is the "+
				"credential for this driver: %s", secret, blob)
		}
	}

	// …and the key NAMES, which are not secret and are what a Sink declaration needs, ARE there.
	for _, want := range []string{"hook_url", "channels", "token", "slack", "slack-ops"} {
		if !strings.Contains(blob, want) {
			t.Errorf("the projection dropped %q — the key names say which driver knobs were "+
				"configured, which is exactly what has to be re-declared as a Sink: %s", want, blob)
		}
	}
}

// The discard is in UnmarshalJSON on purpose: decoding into a map and filtering later would
// leave the values reachable for as long as the snapshot lives, and one edit away from a
// facet. There must be nothing in the struct to leak.
func TestConfigKeysKeepsOnlyNamesAndIsSorted(t *testing.T) {
	var k configKeys
	if err := k.UnmarshalJSON([]byte(`{"zeta":"s3cr3t","alpha":1,"mu":{"nested":"also secret"}}`)); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(k)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `["alpha","mu","zeta"]` {
		t.Fatalf("configKeys = %s — names only, and sorted so a facet that reorders between "+
			"polls does not read as a change", got)
	}
}

// A mirror must not die on a driver we have not seen. AWX's configuration shape is
// driver-defined, so null / a scalar / an array are "no keys", not a failed projection —
// the whole Observe fails on an error here and would take nine good collections with it.
func TestOddConfigurationShapesAreNoKeysNotAFailure(t *testing.T) {
	for _, doc := range []string{`null`, `"a string"`, `[1,2,3]`, `42`} {
		var k configKeys
		if err := k.UnmarshalJSON([]byte(doc)); err != nil {
			t.Errorf("%s must decode to no keys, not an error: %v", doc, err)
		}
		if len(k) != 0 {
			t.Errorf("%s produced keys %v", doc, k)
		}
	}
}

// messagesCustomized is presence, never content — and `null` is the AWX default for an
// uncustomized template, so it must not read as customized.
func TestMessagesCustomizedIsPresenceOnly(t *testing.T) {
	c := &Client{ctrlID: "ctrl-a"}
	facetOf := func(messages string) map[string]any {
		t.Helper()
		var nt NotificationTemplate
		if err := json.Unmarshal([]byte(`{"id":1,"name":"n","notification_type":"webhook","messages":`+messages+`}`), &nt); err != nil {
			t.Fatal(err)
		}
		ents, err := c.Normalize(&Snapshot{Notifications: []NotificationTemplate{nt}})
		if err != nil {
			t.Fatal(err)
		}
		var facet map[string]any
		if err := json.Unmarshal(ents[0].GetFacets()[KindNotification], &facet); err != nil {
			t.Fatal(err)
		}
		return facet
	}
	if facetOf(`null`)["messagesCustomized"] != false {
		t.Error("AWX's default is null; an uncustomized template must not report customized")
	}
	if facetOf(`{"error":{"body":"x"}}`)["messagesCustomized"] != true {
		t.Error("a hand-written body is a cutover fact and must be reported")
	}
}

// The mirror is a graph, not a list: a notification template belongs to an org, exactly as
// templates and teams do, so "what does this org send, and where" is a traversal.
func TestNotificationIsOwnedByItsOrg(t *testing.T) {
	c := &Client{ctrlID: "ctrl-a"}
	var nt NotificationTemplate
	if err := json.Unmarshal([]byte(`{"id":90,"name":"n","notification_type":"slack",
		"summary_fields":{"organization":{"id":1,"name":"Platform"}}}`), &nt); err != nil {
		t.Fatal(err)
	}
	ents, err := c.Normalize(&Snapshot{Notifications: []NotificationTemplate{nt}})
	if err != nil {
		t.Fatal(err)
	}
	rels := ents[0].GetRelations()
	if len(rels) != 1 || rels[0].GetType() != "owned-by" || rels[0].GetToScheme() != KindOrg {
		t.Fatalf("org edge missing: %v", rels)
	}
	if ents[0].GetIdentityKeys()[KindNotification] != "ctrl-a/90" {
		t.Errorf("identity must be controller-qualified so two Controllers never collide: %v",
			ents[0].GetIdentityKeys())
	}
}
