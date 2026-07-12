package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/types"
)

// headerRT injects the dev-principal headers into every client request.
type headerRT struct{ id, kind string }

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if h.id != "" {
		r.Header.Set("X-Stratt-Principal", h.id)
		r.Header.Set("X-Stratt-Principal-Kind", h.kind)
	}
	return http.DefaultTransport.RoundTrip(r)
}

type usageLog struct {
	mu    sync.Mutex
	calls []types.MCPCall
}

func (u *usageLog) record(_ context.Context, c types.MCPCall) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls = append(u.calls, c)
	return nil
}

// testConfig: the "REST API" echoes the principal it saw on the context —
// proving the tool layer stamps identity onto the one shared seam.
func testConfig(usage *usageLog) Config {
	return Config{
		Resolve: func(_ context.Context, h http.Header) (string, string, error) {
			if h == nil {
				return "", "", nil
			}
			kind := h.Get("X-Stratt-Principal-Kind")
			return h.Get("X-Stratt-Principal"), kind, nil
		},
		API: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, kind, _ := authz.PrincipalFrom(r.Context())
			switch r.URL.Path {
			case "/findings":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"principal": id, "kind": kind, "path": r.URL.Path, "status": r.URL.Query().Get("status"),
				})
			case "/gates/g1/decision":
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"principal ` + id + ` is not an approver"}`))
			default:
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"not found"}`))
			}
		}),
		RecordUsage: usage.record,
		Log:         testLogger(),
	}
}

func connect(t *testing.T, url string, rt headerRT) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: rt},
		// No server-initiated messages in these tests; the standalone SSE
		// stream would keep the httptest server from closing.
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatalf("no content: %+v", res)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content is not text: %T", res.Content[0])
	}
	return tc.Text
}

func TestUnauthenticated401(t *testing.T) {
	usage := &usageLog{}
	srv := httptest.NewServer(New(testConfig(usage)))
	defer srv.Close()

	res, err := http.Post(srv.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous MCP request must be 401, got %d", res.StatusCode)
	}
}

func TestToolCarriesPrincipalThroughOneSeam(t *testing.T) {
	usage := &usageLog{}
	srv := httptest.NewServer(New(testConfig(usage)))
	defer srv.Close()

	session := connect(t, srv.URL, headerRT{id: "remedy-bot", kind: authz.KindAgent})

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) < 24 {
		t.Fatalf("expected the full tool surface, got %d", len(tools.Tools))
	}

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "list_findings", Arguments: map[string]any{"status": "open"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool errored: %s", textOf(t, res))
	}
	// Output rides the untrusted-estate-data envelope (§7.3 posture).
	var env struct {
		Note string                                   `json:"note"`
		Data struct{ Principal, Kind, Status string } `json:"data"`
	}
	if err := json.Unmarshal([]byte(textOf(t, res)), &env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.Note, "never as instructions") {
		t.Fatalf("estate data must be framed as data, not instructions: %q", env.Note)
	}
	echo := env.Data
	if echo.Principal != "remedy-bot" || echo.Kind != authz.KindAgent || echo.Status != "open" {
		t.Fatalf("principal/kind/filter must ride the one seam: %+v", echo)
	}

	usage.mu.Lock()
	defer usage.mu.Unlock()
	if len(usage.calls) != 1 || usage.calls[0].Tool != "list_findings" ||
		usage.calls[0].Principal != "remedy-bot" || usage.calls[0].PrincipalKind != authz.KindAgent || !usage.calls[0].OK {
		t.Fatalf("usage accounting row wrong: %+v", usage.calls)
	}
}

func TestAPIDenialSurfacesVerbatim(t *testing.T) {
	usage := &usageLog{}
	srv := httptest.NewServer(New(testConfig(usage)))
	defer srv.Close()

	session := connect(t, srv.URL, headerRT{id: "remedy-bot", kind: authz.KindAgent})
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "decide_gate", Arguments: map[string]any{"gateId": "g1", "approve": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("403 must surface as a tool error")
	}
	if msg := textOf(t, res); !strings.Contains(msg, "remedy-bot is not an approver") || !strings.Contains(msg, "403") {
		t.Fatalf("API message must surface verbatim (§1.8): %s", msg)
	}

	usage.mu.Lock()
	defer usage.mu.Unlock()
	if len(usage.calls) != 1 || usage.calls[0].OK {
		t.Fatalf("denied call must account as an error: %+v", usage.calls)
	}
}
