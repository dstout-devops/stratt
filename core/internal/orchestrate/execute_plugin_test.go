package orchestrate

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	"github.com/dstout-devops/stratt/types"
)

// TestExecutePlugin_DryRunRefusedCoreSide proves guardian fix #6: a non-dry-runnable
// plugin Actuator refuses dry-run CORE-SIDE from the reconciled capability — the
// plugin (nil client) is never dialed.
func TestExecutePlugin_DryRunRefusedCoreSide(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	host := pluginhost.New(nil, nil, pluginhost.Grant{Source: types.Source{Name: "opentofu"}}, discard)
	a := &Activities{PluginActuators: map[string]PluginActuator{"opentofu": {Host: host, DryRunnable: false}}}
	_, err := a.Execute(context.Background(), RunInput{Actuator: "opentofu", DryRun: true}, 0, "", ResolvedTargets{}, nil)
	if err == nil || !strings.Contains(err.Error(), "does not support dry-run") {
		t.Fatalf("non-dry-runnable plugin actuator must refuse dry-run core-side, got %v", err)
	}
}

// TestExecutePlugin_ZeroCredsNotUngated proves guardian fix #4: an actuation Step's
// authz chokepoint is the runner-on-View grant (not the Action credential
// use-check), so a plugin actuator with ZERO creds is NOT refused for lacking them.
// With a non-local Site the path stops at the (deliberate) hub-only guard — proving
// it proceeded PAST credential-gating on zero creds (the Action path would have
// refused "must carry a CredentialRef" first).
func TestExecutePlugin_ZeroCredsNotUngated(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	host := pluginhost.New(nil, nil, pluginhost.Grant{Source: types.Source{Name: "opentofu"}}, discard)
	a := &Activities{PluginActuators: map[string]PluginActuator{"opentofu": {Host: host, DryRunnable: true}}}
	_, err := a.Execute(context.Background(), RunInput{Actuator: "opentofu"}, 0, "remote-1", ResolvedTargets{}, nil)
	if err == nil || !strings.Contains(err.Error(), "remote Site execution is not yet supported") {
		t.Fatalf("zero-cred plugin actuation must pass credential-gating (guardian #4), got %v", err)
	}
	if strings.Contains(err.Error(), "CredentialRef") || strings.Contains(err.Error(), "Ungated") {
		t.Fatalf("actuation must NOT inherit the Action path's ungated-credential refusal, got %v", err)
	}
}
