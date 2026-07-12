package graph

import (
	"context"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func TestUsageAccounting(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	for _, c := range []types.MCPCall{
		{Principal: "remedy-bot", PrincipalKind: "agent", Tool: "list_findings", OK: true, DurationMS: 12},
		{Principal: "remedy-bot", PrincipalKind: "agent", Tool: "list_findings", OK: true, DurationMS: 9},
		{Principal: "remedy-bot", PrincipalKind: "agent", Tool: "decide_gate", OK: false, DurationMS: 3},
		{Principal: "admin", PrincipalKind: "human", Tool: "list_findings", OK: true, DurationMS: 5},
	} {
		if err := store.RecordMCPCall(ctx, c); err != nil {
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
