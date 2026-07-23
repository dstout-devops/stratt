package orchestrate

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	"github.com/dstout-devops/stratt/types"
)

// TestExecuteAction_UngatedRefused proves the §2.5/§1.6 credential gate fires
// BEFORE the pod-vs-plugin branch: a credential-free Action is refused on either
// path, so a plugin Action can never be a weaker authz path than a pod Action.
func TestExecuteAction_UngatedRefused(t *testing.T) {
	a := &Activities{}
	_, err := a.ExecuteAction(context.Background(), RunInput{Action: "x/y"}, nil)
	if err == nil || !strings.Contains(err.Error(), "must carry a CredentialRef") {
		t.Fatalf("credential-free Action must be refused (ActionUngated), got %v", err)
	}
}

// TestExecuteAction_PluginDryRunRefusedCoreSide proves dry-run is refused
// core-side from the reconciled ActionDecl (dry_runnable=false) — never delegated
// to the plugin, which is never dialed here.
func TestExecuteAction_PluginDryRunRefusedCoreSide(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	host := pluginhost.New(nil, nil, pluginhost.Grant{Source: types.Source{Name: "x"}}, discard)
	a := &Activities{Plugins: NewPluginRegistryWith(nil, map[string]PluginAction{"x/y": {Host: host, DryRunnable: false}})}
	_, err := a.ExecuteAction(context.Background(), RunInput{Action: "x/y", DryRun: true},
		[]dispatch.CredentialMount{{RefName: "c"}})
	if err == nil || !strings.Contains(err.Error(), "does not support dry-run") {
		t.Fatalf("non-dry-runnable plugin Action must be refused core-side, got %v", err)
	}
}
