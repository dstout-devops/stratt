package awsec2

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// TestLiveLifecycleAgainstFloci exercises the FULL instance lifecycle against a real
// EC2 backend (Floci, ADR-0093/0095) through the plugin's own port surface — the dev
// proof that create/observe/reboot/tag/stop/terminate all drive real state, not a mock.
// Gated on STRATT_LIVE_EC2_ENDPOINT so it is a no-op in normal `task ci`. Run with:
//
//	STRATT_LIVE_EC2_ENDPOINT=http://localhost:4566 AWS_ACCESS_KEY_ID=testing \
//	  AWS_SECRET_ACCESS_KEY=testing AWS_REGION=us-east-1 \
//	  go test ./ -run LiveLifecycle -v
func TestLiveLifecycleAgainstFloci(t *testing.T) {
	endpoint := os.Getenv("STRATT_LIVE_EC2_ENDPOINT")
	if endpoint == "" {
		t.Skip("set STRATT_LIVE_EC2_ENDPOINT (the Floci endpoint) to run the live EC2 lifecycle proof")
	}
	srv := NewServer(Config{Region: "us-east-1", Endpoint: endpoint}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// invoke runs one Action and returns the terminal InvokeResult (fatal on not-ok).
	invoke := func(action string, args any) *pluginv1.InvokeResult {
		t.Helper()
		raw, _ := json.Marshal(args)
		stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
		if err := srv.Invoke(&pluginv1.InvokeRequest{Action: action, Args: &pluginv1.Payload{Bytes: raw}}, stream); err != nil {
			t.Fatalf("%s transport error: %v", action, err)
		}
		term := stream.sent[len(stream.sent)-1]
		if !term.GetEvent().GetTerminal() || !term.GetEvent().GetOk() {
			t.Fatalf("%s not terminal-ok: %q", action, term.GetEvent().GetMessage())
		}
		return term.GetResult()
	}

	// 1. create-vm → a real Floci instance.
	res := invoke(actionCreateVM, createVMParams{Region: "us-east-1", AMI: "ami-demo", InstanceType: "t3.micro", Name: "c1-live"})
	var created map[string]any
	_ = json.Unmarshal(res.GetOutputs().GetBytes(), &created)
	id, _ := created["instanceId"].(string)
	if id == "" {
		t.Fatal("create-vm returned no instanceId")
	}
	t.Logf("LIVE created %s", id)
	defer invoke(actionTerminate, lifecycleParams{InstanceID: id}) // best-effort cleanup

	// 2. Observe → the instance is enumerable as an Entity with validated facets.
	ostream := &captureStream[pluginv1.ObserveResponse]{ctx: context.Background()}
	if err := srv.Observe(&pluginv1.ObserveRequest{}, ostream); err != nil {
		t.Fatalf("observe: %v", err)
	}
	found := false
	for _, resp := range ostream.sent {
		for _, e := range resp.GetEntities() {
			if e.GetIdentityKeys()["aws.instanceId"] == id {
				found = true
				if len(e.GetFacets()["instance.compute"]) == 0 {
					t.Errorf("observed instance missing instance.compute facet")
				}
			}
		}
	}
	if !found {
		t.Fatalf("Observe did not project the just-created instance %s", id)
	}
	t.Logf("LIVE observed %s", id)

	// 3. reboot · 4. tag · 5. stop — each a real transition/mutation.
	invoke(actionReboot, lifecycleParams{InstanceID: id})
	invoke(actionTag, tagParams{InstanceID: id, Tags: map[string]string{"slice": "c1", "env": "dev"}})
	stopRes := invoke(actionStop, lifecycleParams{InstanceID: id})
	var stopped map[string]any
	_ = json.Unmarshal(stopRes.GetOutputs().GetBytes(), &stopped)
	t.Logf("LIVE reboot+tag ok; stop → state=%v", stopped["state"])

	// 6. terminate (explicit; the defer is a safety net).
	termRes := invoke(actionTerminate, lifecycleParams{InstanceID: id})
	var terminated map[string]any
	_ = json.Unmarshal(termRes.GetOutputs().GetBytes(), &terminated)
	t.Logf("LIVE terminated %s → state=%v", id, terminated["state"])
}
