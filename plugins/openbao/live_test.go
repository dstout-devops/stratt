package openbao

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// TestLivePKIAgainstOpenBao exercises the E2 PKI surface against a real OpenBao PKI
// (ADR-0098). It observes the seeded root CA as a `ca` Entity, provisions an
// intermediate (fail-closed on re-invoke), and rotates the CRL. Gated on
// STRATT_LIVE_OPENBAO_ADDR. Run with:
//
//	STRATT_LIVE_OPENBAO_ADDR=http://localhost:8200 STRATT_LIVE_OPENBAO_TOKEN=stratt-dev-root \
//	  go test ./ -run LivePKI -v
func TestLivePKIAgainstOpenBao(t *testing.T) {
	addr := os.Getenv("STRATT_LIVE_OPENBAO_ADDR")
	if addr == "" {
		t.Skip("set STRATT_LIVE_OPENBAO_ADDR (+ STRATT_LIVE_OPENBAO_TOKEN) to run the live PKI proof")
	}
	token := os.Getenv("STRATT_LIVE_OPENBAO_TOKEN")
	const intMount = "pki_int_live"
	// Clean any prior intermediate mount so create-intermediate starts fresh.
	req, _ := http.NewRequest(http.MethodDelete, addr+"/v1/sys/mounts/"+intMount, nil)
	req.Header.Set("X-Vault-Token", token)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
	}

	srv := NewServer(Config{Addr: addr, Token: token, Mount: "pki", IntMount: intMount},
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	// 1. Observe — the seeded root CA projects as a `ca` Entity.
	ostream := &captureStream[pluginv1.ObserveResponse]{ctx: context.Background()}
	if err := srv.Observe(&pluginv1.ObserveRequest{}, ostream); err != nil {
		t.Fatalf("observe: %v", err)
	}
	kinds := map[string]int{}
	for _, resp := range ostream.sent {
		for _, e := range resp.GetEntities() {
			kinds[e.GetKind()]++
		}
	}
	t.Logf("LIVE observed kinds: %v", kinds)
	if kinds["ca"] == 0 {
		t.Fatal("Observe must project the root CA as a ca Entity")
	}

	invoke := func(action string, args any) *pluginv1.InvokeResponse {
		raw, _ := json.Marshal(args)
		st := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
		if err := srv.Invoke(&pluginv1.InvokeRequest{Action: action, Args: &pluginv1.Payload{Bytes: raw}}, st); err != nil {
			t.Fatalf("%s transport: %v", action, err)
		}
		return st.sent[len(st.sent)-1]
	}

	// 2. create-intermediate — succeeds on the fresh int mount.
	term := invoke("cert-issuer/create-intermediate", map[string]any{"commonName": "Stratt Dev Intermediate (live)"})
	if !term.GetEvent().GetOk() {
		t.Fatalf("create-intermediate should succeed: %q", term.GetEvent().GetMessage())
	}
	var out map[string]any
	_ = json.Unmarshal(term.GetResult().GetOutputs().GetBytes(), &out)
	t.Logf("LIVE created intermediate CA serial=%v", out["caSerial"])
	if out["caSerial"] == "" || out["caSerial"] == nil {
		t.Fatal("create-intermediate returned no caSerial")
	}

	// 3. create-intermediate again — FAILS CLOSED (a CA now exists).
	if again := invoke("cert-issuer/create-intermediate", map[string]any{"commonName": "dup"}); again.GetEvent().GetOk() {
		t.Fatal("create-intermediate must FAIL CLOSED when the intermediate CA already exists")
	}
	t.Logf("LIVE re-invoke correctly failed closed")

	// 4. rotate-crl.
	if r := invoke("cert-issuer/rotate-crl", map[string]any{}); !r.GetEvent().GetOk() {
		t.Fatalf("rotate-crl should succeed: %q", r.GetEvent().GetMessage())
	}
	t.Logf("LIVE rotate-crl ok")

	// cleanup
	req2, _ := http.NewRequest(http.MethodDelete, addr+"/v1/sys/mounts/"+intMount, nil)
	req2.Header.Set("X-Vault-Token", token)
	if resp, err := http.DefaultClient.Do(req2); err == nil {
		_ = resp.Body.Close()
	}
}

// TestLiveKVMetadataAgainstOpenBao proves the KV metadata Syncer against real OpenBao
// (ADR-0099): the secret/demo/aws secret (seeded by openbao-bootstrap) projects as a
// kv-secret Entity with metadata (version/timestamps) and NEVER its values. Gated on
// STRATT_LIVE_OPENBAO_ADDR.
func TestLiveKVMetadataAgainstOpenBao(t *testing.T) {
	addr := os.Getenv("STRATT_LIVE_OPENBAO_ADDR")
	if addr == "" {
		t.Skip("set STRATT_LIVE_OPENBAO_ADDR to run the live KV metadata proof")
	}
	srv := NewServer(Config{Addr: addr, Token: os.Getenv("STRATT_LIVE_OPENBAO_TOKEN"), Mount: "pki", KVMount: "secret"},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	stream := &captureStream[pluginv1.ObserveResponse]{ctx: context.Background()}
	if err := srv.Observe(&pluginv1.ObserveRequest{}, stream); err != nil {
		t.Fatalf("observe: %v", err)
	}
	var demo *pluginv1.ObservedEntity
	for _, resp := range stream.sent {
		for _, e := range resp.GetEntities() {
			if e.GetKind() == "kv-secret" && e.GetIdentityKeys()["kv.path"] == "secret/demo/aws" {
				demo = e
			}
		}
	}
	if demo == nil {
		t.Fatal("Observe did not project the seeded KV secret secret/demo/aws as a kv-secret")
	}
	var md map[string]any
	_ = json.Unmarshal(demo.GetFacets()["kv.metadata"], &md)
	t.Logf("LIVE kv-secret secret/demo/aws metadata: %v", md)
	// The seeded secret's values (access_key, secret_key) must NEVER appear.
	blob, _ := json.Marshal(md)
	for _, forbidden := range []string{"access_key", "secret_key", "AKIADEMO", "dev/secret/material"} {
		if strings.Contains(string(blob), forbidden) {
			t.Fatalf("§2.5 VIOLATION: kv.metadata leaked secret material (%q): %s", forbidden, blob)
		}
	}
	if _, ok := md["currentVersion"]; !ok {
		t.Error("kv.metadata should carry currentVersion")
	}
}
