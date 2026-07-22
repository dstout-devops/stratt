package secretbroker

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// vaultClient is a tiny OpenBao/Vault KV reader over net/http — the plugins/certissuer
// precedent (no SDK dependency, §1.4). It authenticates as the plugin ITSELF (its own
// token, a policy scoped to the granted paths — MF-A); the core never sees it. Only
// the KV read slice of the API is here; the resolver needs nothing more.
type vaultClient struct {
	addr  string // base address, no trailing slash (e.g. http://openbao:8200)
	token string // the plugin's own token (dev: root; prod: AppRole/K8s-auth, ADR-0094)
	hc    *http.Client
}

func newVaultClient(addr, token string) *vaultClient {
	return &vaultClient{
		addr:  strings.TrimRight(addr, "/"),
		token: token,
		hc:    &http.Client{Timeout: 10 * time.Second},
	}
}

// readKV reads the KV secret at coords and returns ONLY the requested fields, each as
// a FRESH []byte the caller owns and zeroizes (MF-B). Values are decoded straight into
// []byte via jsonStringToBytes — never an intermediate Go string, which would linger
// un-zeroizable on the heap (ADR-0094). The response body buffer is wiped before return.
func (v *vaultClient) readKV(ctx context.Context, coords *pluginv1.VaultCoords, fields []string) (map[string][]byte, error) {
	// KV v2 nests the secret under /data/; v1 reads it directly. LIST/metadata are
	// never touched — this is a material read, confined to the granted path.
	mount, path := coords.GetMount(), coords.GetPath()
	rel := mount + "/" + path
	if coords.GetKvV2() {
		rel = mount + "/data/" + path
	}
	url := v.addr + "/v1/" + rel
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", v.token)
	resp, err := v.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(body) // the raw material was in this buffer — wipe it (MF-B)
	if resp.StatusCode != http.StatusOK {
		// Never echo the body (it may carry material) — status only.
		return nil, fmt.Errorf("vault KV read returned %d", resp.StatusCode)
	}
	// {"data": <v1 fields> } or {"data": {"data": <v2 fields>, "metadata": …}}.
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("vault KV response not JSON: %w", err)
	}
	dataRaw := envelope.Data
	if coords.GetKvV2() {
		var inner struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(envelope.Data, &inner); err != nil {
			return nil, fmt.Errorf("vault KV v2 data wrapper malformed: %w", err)
		}
		dataRaw = inner.Data
	}
	// Parse the field map as RawMessages (each a copied []byte we own), then decode
	// ONLY the authorized fields into zeroizable []byte.
	var kv map[string]json.RawMessage
	if err := json.Unmarshal(dataRaw, &kv); err != nil {
		return nil, fmt.Errorf("vault KV data not an object: %w", err)
	}
	out := make(map[string][]byte, len(fields))
	for _, f := range fields {
		raw, ok := kv[f]
		if !ok {
			continue // the resolver reports the missing field with mount/path context
		}
		b, err := jsonStringToBytes(raw)
		if err != nil {
			return nil, fmt.Errorf("vault KV field %q: %w", f, err)
		}
		out[f] = b
	}
	return out, nil
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// jsonStringToBytes decodes a JSON string value (a json.RawMessage like `"abc"`) into a
// FRESH []byte the caller owns and can zeroize — WITHOUT producing an intermediate Go
// string (which would linger un-zeroizable on the heap, ADR-0094 MF-B). KV field values
// are JSON strings; a non-string value (number/object) is rejected — the plugin asked
// for credential material, not structure.
func jsonStringToBytes(raw []byte) ([]byte, error) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return nil, fmt.Errorf("KV field is not a JSON string")
	}
	inner := raw[1 : len(raw)-1]
	// Fast path: no escapes — the overwhelming common case for tokens/passwords.
	if bytes.IndexByte(inner, '\\') < 0 {
		return append([]byte(nil), inner...), nil
	}
	out := make([]byte, 0, len(inner))
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		i++
		if i >= len(inner) {
			return nil, fmt.Errorf("KV field has a dangling escape")
		}
		switch inner[i] {
		case '"':
			out = append(out, '"')
		case '\\':
			out = append(out, '\\')
		case '/':
			out = append(out, '/')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			if i+4 >= len(inner) {
				return nil, fmt.Errorf("KV field has a truncated \\u escape")
			}
			var buf [2]byte
			if _, err := hex.Decode(buf[:], inner[i+1:i+5]); err != nil {
				return nil, fmt.Errorf("KV field has a bad \\u escape: %w", err)
			}
			out = utf8.AppendRune(out, rune(uint16(buf[0])<<8|uint16(buf[1])))
			i += 4
		default:
			return nil, fmt.Errorf("KV field has an invalid escape \\%c", inner[i])
		}
	}
	return out, nil
}
