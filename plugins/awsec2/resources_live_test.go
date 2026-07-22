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

// TestLiveResourcesAgainstFloci provisions each fire-and-return resource Action against
// a real EC2 backend (Floci, ADR-0095 C2) through the plugin's port. Gated on
// STRATT_LIVE_EC2_ENDPOINT (a no-op in normal `task ci`). Run with:
//
//	STRATT_LIVE_EC2_ENDPOINT=http://localhost:4566 AWS_ACCESS_KEY_ID=testing \
//	  AWS_SECRET_ACCESS_KEY=testing AWS_REGION=us-east-1 \
//	  go test ./ -run LiveResources -v
func TestLiveResourcesAgainstFloci(t *testing.T) {
	endpoint := os.Getenv("STRATT_LIVE_EC2_ENDPOINT")
	if endpoint == "" {
		t.Skip("set STRATT_LIVE_EC2_ENDPOINT to run the live resource-provisioning proof")
	}
	srv := NewServer(Config{Region: "us-east-1", Endpoint: endpoint}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	run := func(action string, args any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(args)
		stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
		if err := srv.Invoke(&pluginv1.InvokeRequest{Action: action, Args: &pluginv1.Payload{Bytes: raw}}, stream); err != nil {
			t.Fatalf("%s transport: %v", action, err)
		}
		term := stream.sent[len(stream.sent)-1]
		if !term.GetEvent().GetTerminal() || !term.GetEvent().GetOk() {
			t.Fatalf("%s not ok: %q", action, term.GetEvent().GetMessage())
		}
		var m map[string]any
		_ = json.Unmarshal(term.GetResult().GetOutputs().GetBytes(), &m)
		return m
	}

	vpc := run("awsec2/create-vpc", map[string]any{"cidrBlock": "10.42.0.0/16"})["vpcId"].(string)
	t.Logf("LIVE vpc=%s", vpc)
	subnet := run("awsec2/create-subnet", map[string]any{"vpcId": vpc, "cidrBlock": "10.42.1.0/24"})["subnetId"].(string)
	t.Logf("LIVE subnet=%s", subnet)
	sg := run("awsec2/create-security-group", map[string]any{"groupName": "c2-live", "description": "c2 live sg", "vpcId": vpc})["securityGroupId"].(string)
	t.Logf("LIVE securityGroup=%s", sg)
	vol := run("awsec2/create-volume", map[string]any{"availabilityZone": "us-east-1a", "sizeGiB": 1})["volumeId"].(string)
	t.Logf("LIVE volume=%s", vol)

	if vpc == "" || subnet == "" || sg == "" || vol == "" {
		t.Fatal("a live resource id came back empty")
	}
	// import-key-pair is best-effort against Floci (it may not implement ImportKeyPair);
	// the unit test covers the §2.5 public-key path deterministically.
	pub := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHZ4Xn1exampleexampleexampleexampleexampleAB stratt-c2-live"
	kstream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
	kargs, _ := json.Marshal(map[string]any{"keyName": "c2-live", "publicKey": pub})
	if err := srv.Invoke(&pluginv1.InvokeRequest{Action: "awsec2/import-key-pair", Args: &pluginv1.Payload{Bytes: kargs}}, kstream); err == nil {
		term := kstream.sent[len(kstream.sent)-1]
		t.Logf("LIVE import-key-pair terminal ok=%v msg=%q", term.GetEvent().GetOk(), term.GetEvent().GetMessage())
	}
}
