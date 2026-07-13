package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/core/internal/rules"
	"github.com/dstout-devops/stratt/types"
)

// fakeAuthz is a stub Authorizer for the credential-use gate.
type fakeAuthz struct {
	allow bool
	err   error
}

func (f fakeAuthz) Check(_ context.Context, _, _, _ string) (bool, error) { return f.allow, f.err }

func TestKindListed(t *testing.T) {
	on := []string{types.NoticeRunFailed, types.NoticeFindingOpen}
	if !kindListed(on, types.NoticeFindingOpen) {
		t.Fatal("listed kind must match")
	}
	if kindListed(on, types.NoticeGatePending) {
		t.Fatal("unlisted kind must not match")
	}
}

func TestRenderBodyDefault(t *testing.T) {
	n := types.Notice{Kind: types.NoticeRunFailed, Subject: "run-1", At: time.Unix(0, 0).UTC(),
		Payload: map[string]any{"view": "prod", "failed": 2}}
	body, err := renderBody(types.Sink{Name: "s"}, n)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("default body must be JSON: %v", err)
	}
	if got["kind"] != types.NoticeRunFailed || got["subject"] != "run-1" {
		t.Fatalf("default body missing fields: %v", got)
	}
}

func TestRenderBodyTemplate(t *testing.T) {
	sink := types.Sink{Name: "slack", Config: types.SinkConfig{
		BodyTemplate: `{"text":"run {{.subject}} failed on {{.payload.view}}"}`,
	}}
	n := types.Notice{Kind: types.NoticeRunFailed, Subject: "run-7", Payload: map[string]any{"view": "prod"}}
	body, err := renderBody(sink, n)
	if err != nil {
		t.Fatal(err)
	}
	if body != `{"text":"run run-7 failed on prod"}` {
		t.Fatalf("template body = %q", body)
	}
}

// TestMatchAdditive proves the CEL match gate: an empty match passes every
// notice of a listed kind; a predicate selects on payload; and — the §2.4
// point — two independent Subscriptions that both match one Notice both pass
// (additive fan-out, no precedence). handle() delivers to each; here we assert
// the matching decision each makes in isolation.
func TestMatchAdditive(t *testing.T) {
	d := &Dispatcher{programs: map[string]*rules.Program{}, specs: map[string]string{}}
	crit := types.Subscription{Name: "crit", On: []string{types.NoticeFindingOpen},
		Match: `event.payload.severity == "critical"`, Sink: "a"}
	all := types.Subscription{Name: "all", On: []string{types.NoticeFindingOpen}, Sink: "b"}

	n := types.Notice{Kind: types.NoticeFindingOpen, Subject: "bl/host1",
		Payload: map[string]any{"severity": "critical"}}

	if ok, err := d.matches(crit, n); err != nil || !ok {
		t.Fatalf("critical predicate should match: ok=%v err=%v", ok, err)
	}
	if ok, err := d.matches(all, n); err != nil || !ok {
		t.Fatalf("empty match should match: ok=%v err=%v", ok, err)
	}
	// A non-critical notice: the predicate sub drops it, the catch-all keeps it.
	low := types.Notice{Kind: types.NoticeFindingOpen, Subject: "bl/host2",
		Payload: map[string]any{"severity": "warning"}}
	if ok, _ := d.matches(crit, low); ok {
		t.Fatal("critical predicate must not match a warning")
	}
	if ok, _ := d.matches(all, low); !ok {
		t.Fatal("catch-all must still match a warning")
	}
}

// TestResolveCredentialAuthz proves the §1.6/§2.5 delivery credential gate: a
// Sink with no Principal is refused, and a Principal that lacks `use` on the
// CredentialRef is denied — both BEFORE the Store is ever touched (nil Store
// here proves the check short-circuits ahead of any credential resolution).
func TestResolveCredentialAuthz(t *testing.T) {
	ctx := context.Background()

	noPrincipal := &Dispatcher{Authz: fakeAuthz{allow: true}}
	if _, err := noPrincipal.resolveCredential(ctx, types.Sink{Name: "s", CredentialRef: "c"}); err == nil ||
		!strings.Contains(err.Error(), "principal is required") {
		t.Fatalf("missing principal must be refused, got %v", err)
	}

	denied := &Dispatcher{Authz: fakeAuthz{allow: false}}
	_, err := denied.resolveCredential(ctx, types.Sink{Name: "s", Principal: "p", CredentialRef: "c"})
	if err == nil || !strings.Contains(err.Error(), "lacks use on credential_ref:c") {
		t.Fatalf("ungranted principal must be denied, got %v", err)
	}
}
