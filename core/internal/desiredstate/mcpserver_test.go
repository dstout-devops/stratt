package desiredstate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func writeMCPServer(t *testing.T, root, file, content string) {
	t.Helper()
	dir := filepath.Join(root, "mcp-servers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseMCPServers(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
	writeMCPServer(t, root, "demo.yaml", `
name: demo-tools
transport: stdio
rev: 1
script: |
  import sys
  # server source, Git-reviewed
`)
	parsed, err := ParseDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.MCPServers) != 1 || parsed.MCPServers[0].Rev != 1 ||
		parsed.MCPServers[0].Transport != types.MCPTransportStdio {
		t.Fatalf("mcp servers: %+v", parsed.MCPServers)
	}

	for name, doc := range map[string]string{
		"missing rev":           "name: x\ntransport: stdio\nscript: s\n",
		"stdio without script":  "name: x\ntransport: stdio\nrev: 1\n",
		"stdio with endpoint":   "name: x\ntransport: stdio\nrev: 1\nscript: s\nendpoint: http://x\n",
		"http without endpoint": "name: x\ntransport: http\nrev: 1\n",
		"http with script":      "name: x\ntransport: http\nrev: 1\nendpoint: http://x\nscript: s\n",
		"bad transport":         "name: x\ntransport: grpc\nrev: 1\n",
		"partial tokenRef":      "name: x\ntransport: http\nrev: 1\nendpoint: http://x\ntokenRef: {credentialRef: c}\n",
	} {
		bad := t.TempDir()
		writeDecl(t, bad, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
		writeMCPServer(t, bad, "x.yaml", doc)
		if _, err := ParseDir(bad); err == nil {
			t.Fatalf("invalid mcp server (%s) must be rejected", name)
		}
	}
}

func TestMCPServerPlanApplyLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	m := types.MCPServer{Name: "demo-tools", Transport: types.MCPTransportStdio, Rev: 1, Script: "print()"}
	decls := Declarations{MCPServers: []types.MCPServer{m}}

	plan, err := ComputePlan(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	if got := entryFor(plan, KindMCPServer, "demo-tools"); got == nil || got.Action != ActionCreate {
		t.Fatalf("want create, got %+v", plan.Entries)
	}
	if _, err := Apply(ctx, s, decls); err != nil {
		t.Fatal(err)
	}
	plan, _ = ComputePlan(ctx, s, decls)
	if got := entryFor(plan, KindMCPServer, "demo-tools"); got == nil || got.Action != ActionNoop {
		t.Fatalf("want noop, got %+v", plan.Entries)
	}
	decls.MCPServers[0].Rev = 2
	plan, _ = ComputePlan(ctx, s, decls)
	if got := entryFor(plan, KindMCPServer, "demo-tools"); got == nil || got.Action != ActionUpdate {
		t.Fatalf("rev bump must be an update, got %+v", plan.Entries)
	}
	realized, err := Apply(ctx, s, Declarations{})
	if err != nil {
		t.Fatal(err)
	}
	if got := entryFor(realized, KindMCPServer, "demo-tools"); got == nil || got.Action != ActionDelete || got.Error != "" {
		t.Fatalf("want prune, got %+v", realized.Entries)
	}
}
