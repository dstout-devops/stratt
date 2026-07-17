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
// With a non-local Site + no relay configured the path stops at NoPluginRelay —
// proving it proceeded PAST credential-gating on zero creds (the Action path would
// have refused "must carry a CredentialRef" first).
func TestExecutePlugin_ZeroCredsNotUngated(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	host := pluginhost.New(nil, nil, pluginhost.Grant{Source: types.Source{Name: "opentofu"}}, discard)
	a := &Activities{PluginActuators: map[string]PluginActuator{"opentofu": {Host: host, DryRunnable: true}}}
	_, err := a.Execute(context.Background(), RunInput{Actuator: "opentofu"}, 0, "remote-1", ResolvedTargets{}, nil)
	if err == nil || !strings.Contains(err.Error(), "no plugin relay is configured") {
		t.Fatalf("zero-cred plugin actuation must pass credential-gating (guardian #4), got %v", err)
	}
	if strings.Contains(err.Error(), "CredentialRef") || strings.Contains(err.Error(), "Ungated") {
		t.Fatalf("actuation must NOT inherit the Action path's ungated-credential refusal, got %v", err)
	}
}

// TestExecutePlugin_PlanPinMissingFailsClosed proves the runtime fail-closed rule
// (ADR-0047 §8): a plan-pinned Apply (PlanFrom set) with an EMPTY approved digest
// is a terminal error — never a silent unpinned apply of `desired`. The plugin
// (nil client) is never dialed.
func TestExecutePlugin_PlanPinMissingFailsClosed(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	host := pluginhost.New(nil, nil, pluginhost.Grant{Source: types.Source{Name: "opentofu"}}, discard)
	a := &Activities{PluginActuators: map[string]PluginActuator{"opentofu": {Host: host, DryRunnable: true}}}
	_, err := a.Execute(context.Background(),
		RunInput{Actuator: "opentofu", PlanFrom: "plan-step" /* PlanDigest empty */}, 0, "", ResolvedTargets{}, nil)
	if err == nil || !strings.Contains(err.Error(), "refusing an unpinned apply") {
		t.Fatalf("plan-pinned Apply with no approved digest must fail closed, got %v", err)
	}
}
