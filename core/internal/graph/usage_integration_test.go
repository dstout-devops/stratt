package graph

import (
	"context"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func TestUsageAccounting(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	// Usage is now aggregated over the one audit stream (ADR-0034): MCP calls
	// are mcp.tool-call audit events. Record them the way the server does.
	for _, c := range []struct {
		principal, kind, tool string
		ok                    bool
	}{
		{"remedy-bot", "agent", "list_findings", true},
		{"remedy-bot", "agent", "list_findings", true},
		{"remedy-bot", "agent", "decide_gate", false},
		{"admin", "human", "list_findings", true},
	} {
		outcome := types.AuditOK
		if !c.ok {
			outcome = types.AuditFailed
		}
		if err := store.RecordAudit(ctx, types.AuditEvent{
			PrincipalID: c.principal, PrincipalKind: c.kind,
			Action: types.AuditMCPToolCall, Object: c.tool, Outcome: outcome,
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	all, err := store.ListUsage(ctx, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("all usage: %v %+v", err, all)
	}
	bot, err := store.ListUsage(ctx, "remedy-bot")
	if err != nil || len(bot) != 2 {
		t.Fatalf("filtered usage: %v %+v", err, bot)
	}
	byTool := map[string]types.UsageEntry{}
	for _, u := range bot {
		byTool[u.Tool] = u
	}
	if u := byTool["list_findings"]; u.Calls != 2 || u.Errors != 0 || u.PrincipalKind != "agent" {
		t.Fatalf("list_findings aggregate: %+v", u)
	}
	if u := byTool["decide_gate"]; u.Calls != 1 || u.Errors != 1 {
		t.Fatalf("decide_gate aggregate: %+v", u)
	}
}
