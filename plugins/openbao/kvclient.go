package openbao

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// KV is the METADATA-ONLY slice of the KV v2 API this plugin's Syncer needs (ADR-0099).
// There is DELIBERATELY no data-read method: the graph projects that secrets EXIST
// (paths, versions, timestamps) and NEVER their values (§1.2/§2.5). A future refactor
// cannot accidentally read material through this surface. *Client satisfies it.
type KV interface {
	ListKVPaths(ctx context.Context, mount string) ([]string, error)
	GetKVMetadata(ctx context.Context, mount, path string) (KVMetadata, error)
}

// KVMetadata is a KV secret's metadata — never its value.
type KVMetadata struct {
	CurrentVersion int
	CreatedTime    string
	UpdatedTime    string
}

// doList performs a KV v2 LIST (GET ...?list=true) and returns the .data envelope.
func (c *Client) doList(ctx context.Context, v1path string) (json.RawMessage, error) {
	u := c.addr + "/v1/" + strings.TrimPrefix(v1path, "/") + "?list=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", c.token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // an empty/absent path lists as 404 — not an error, just no keys
	}
	var vr vaultResp
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &vr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if len(vr.Errors) > 0 {
			return nil, fmt.Errorf("clm: LIST %s: %d: %s", v1path, resp.StatusCode, strings.Join(vr.Errors, "; "))
		}
		return nil, fmt.Errorf("clm: LIST %s: status %d", v1path, resp.StatusCode)
	}
	return vr.Data, nil
}

// ListKVPaths recursively enumerates every leaf secret path under a KV v2 mount, reading
// ONLY the metadata index (never data). Returns paths relative to the mount.
func (c *Client) ListKVPaths(ctx context.Context, mount string) ([]string, error) {
	return c.walkKV(ctx, mount, "")
}

func (c *Client) walkKV(ctx context.Context, mount, prefix string) ([]string, error) {
	data, err := c.doList(ctx, mount+"/metadata/"+prefix)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var d struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("clm: decode kv list %s: %w", prefix, err)
	}
	var out []string
	for _, k := range d.Keys {
		if strings.HasSuffix(k, "/") {
			// A subtree — recurse. (Bounded by the store's depth.)
			sub, err := c.walkKV(ctx, mount, prefix+k)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
		} else {
			out = append(out, prefix+k)
		}
	}
	return out, nil
}

// GetKVMetadata reads a secret's METADATA (GET {mount}/metadata/{path}) — version count
// + timestamps, NEVER the value. There is no code path here that reads {mount}/data/*.
func (c *Client) GetKVMetadata(ctx context.Context, mount, path string) (KVMetadata, error) {
	data, err := c.doPath(ctx, http.MethodGet, mount+"/metadata/"+path, nil)
	if err != nil {
		return KVMetadata{}, err
	}
	var d struct {
		CurrentVersion int    `json:"current_version"`
		CreatedTime    string `json:"created_time"`
		UpdatedTime    string `json:"updated_time"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return KVMetadata{}, fmt.Errorf("clm: decode kv metadata %s: %w", path, err)
	}
	return KVMetadata{CurrentVersion: d.CurrentVersion, CreatedTime: d.CreatedTime, UpdatedTime: d.UpdatedTime}, nil
}
