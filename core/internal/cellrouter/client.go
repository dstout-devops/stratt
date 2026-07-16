package cellrouter

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// signCellAuth computes the fan-out HMAC signature header value (ADR-0044 slice
// 4): "<ts>:<hex-hmac-sha256(method\npath\nrawQuery\nts)>". Reuses the
// statebackend HMAC idiom. rawQuery is signed so limit/since can't be swapped.
//
// bodyHash is the hex sha256 of the request body, empty for a bodyless request.
// When empty the signed string is byte-identical to the slice-4 GET form
// (method\npath\nrawQuery\nts) — read federation and its tripwires never move.
// When present (a forwarded WRITE, slice 5) it is folded in as a fourth line so
// a tamper/replay-with-swapped-body inside the window can't launch a different
// Run under the forwarded identity (ADR-0044 slice 5, §2.5).
func signCellAuth(secret []byte, method, path, rawQuery, bodyHash string, ts int64) string {
	mac := hmac.New(sha256.New, secret)
	if bodyHash == "" {
		fmt.Fprintf(mac, "%s\n%s\n%s\n%d", method, path, rawQuery, ts)
	} else {
		fmt.Fprintf(mac, "%s\n%s\n%s\n%s\n%d", method, path, rawQuery, bodyHash, ts)
	}
	return strconv.FormatInt(ts, 10) + ":" + hex.EncodeToString(mac.Sum(nil))
}

// hashBody returns the hex sha256 of a request body, or "" for an empty body
// (so a bodyless call signs in the legacy GET form).
func hashBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// verifyCellAuth checks a fan-out signature: correct HMAC (constant-time) AND
// within the replay window. No nonce cache — replay within the window is
// possible but bounded (a GET is idempotent; a forwarded write carries a
// deterministic idempotency key the home Cell dedups on — ADR-0044 slice 5).
// bodyHash binds the write body into the signature (empty for a GET).
func verifyCellAuth(secret []byte, method, path, rawQuery, bodyHash, header string, window time.Duration) bool {
	tsStr, _, ok := strings.Cut(header, ":")
	if !ok {
		return false
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return false
	}
	if d := time.Now().Unix() - ts; d < -int64(window.Seconds()) || d > int64(window.Seconds()) {
		return false
	}
	expected := signCellAuth(secret, method, path, rawQuery, bodyHash, ts)
	return subtle.ConstantTimeCompare([]byte(header), []byte(expected)) == 1
}

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

// doGet performs one GET against a peer's strattd API and returns its status,
// body, and advertised Contract-registry fingerprint. The endpoint comes only
// from the CaC-declared graph.cell registry (never caller input, §2.5).
func (rt *router) doGet(ctx context.Context, endpoint, path, rawQuery string, hdrs map[string]string) (int, []byte, string, error) {
	url := strings.TrimRight(endpoint, "/") + "/api/v1" + path
	if rawQuery != "" {
		url += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, "", err
	}
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	resp, err := rt.http.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return resp.StatusCode, nil, "", fmt.Errorf("cellrouter: read peer body: %w", err)
	}
	return resp.StatusCode, body, resp.Header.Get(hdrRegistryVersion), nil
}

// peerGet is a federated cross-Cell read: it forwards the caller's auth headers
// verbatim (so the peer re-derives the identical Principal — §1.6), marks the
// request as a fan-out (peer serves local-only, no recursion), and HMAC-signs it
// (peer-auth, §... ADR-0044 slice 4).
func (rt *router) peerGet(ctx context.Context, endpoint, path, rawQuery string, fwd map[string]string) (int, []byte, string, error) {
	hdrs := make(map[string]string, len(fwd)+2)
	for k, v := range fwd {
		hdrs[k] = v
	}
	hdrs[fanoutHeader] = "1"
	if len(rt.deps.Secret) > 0 {
		hdrs[authHeader] = signCellAuth(rt.deps.Secret, http.MethodGet, path, rawQuery, "", time.Now().Unix())
	}
	return rt.doGet(ctx, endpoint, path, rawQuery, hdrs)
}

// rawGet is an unauthenticated GET (for the /cellinfo probe).
func (rt *router) rawGet(ctx context.Context, endpoint, path, rawQuery string) (int, []byte, string, error) {
	return rt.doGet(ctx, endpoint, path, rawQuery, nil)
}
