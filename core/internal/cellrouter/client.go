package cellrouter

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// forwardHeaders extracts the caller's auth material from the inbound request to
// replay verbatim on peer calls, so the peer re-derives the IDENTICAL Principal
// (§1.6 one-Principal). Never forwards anything else; anonymous requests forward
// nothing. On the MCP surface these headers are set on the in-process request by
// mcpserver.invoke so this works uniformly.
func forwardHeaders(r *http.Request) map[string]string {
	out := map[string]string{}
	if a := r.Header.Get("Authorization"); a != "" {
		out["Authorization"] = a
	}
	if p := r.Header.Get("X-Stratt-Principal"); p != "" {
		out["X-Stratt-Principal"] = p
		if k := r.Header.Get("X-Stratt-Principal-Kind"); k != "" {
			out["X-Stratt-Principal-Kind"] = k
		}
	}
	return out
}

// peerGet performs one cross-Cell read against a peer's strattd API. It forwards
// the caller's auth headers verbatim (so the peer re-derives the identical
// Principal — §1.6 one-Principal) and marks the request as a fan-out so the peer
// serves it local-only (no recursion). The endpoint comes only from the
// CaC-declared graph.cell registry (never a caller-supplied address, §2.5).
func (rt *router) peerGet(ctx context.Context, endpoint, path, rawQuery string, fwd map[string]string) (int, []byte, error) {
	url := strings.TrimRight(endpoint, "/") + "/api/v1" + path
	if rawQuery != "" {
		url += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range fwd {
		req.Header.Set(k, v)
	}
	req.Header.Set(fanoutHeader, "1")

	resp, err := rt.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("cellrouter: read peer body: %w", err)
	}
	return resp.StatusCode, body, nil
}
