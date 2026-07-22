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

// TestLiveObserveResourceGraph is the C3 end-to-end proof (ADR-0096): provision a VPC
// via the C2 Action (which stamps stratt:managed), then Observe the real Floci account
// and assert the VPC comes back as a `vpc` Entity carrying the stratt.managed label —
// create (C2) → observe-as-Entity (C3), full round-trip against a real backend.
// Gated on STRATT_LIVE_EC2_ENDPOINT.
func TestLiveObserveResourceGraph(t *testing.T) {
	endpoint := os.Getenv("STRATT_LIVE_EC2_ENDPOINT")
	if endpoint == "" {
		t.Skip("set STRATT_LIVE_EC2_ENDPOINT to run the live resource-graph Observe proof")
	}
	srv := NewServer(Config{Region: "us-east-1", Endpoint: endpoint}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Provision a VPC via the C2 Action (stamps stratt:managed).
	args, _ := json.Marshal(map[string]any{"cidrBlock": "10.77.0.0/16"})
	istream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
	if err := srv.Invoke(&pluginv1.InvokeRequest{Action: "awsec2/create-vpc", Args: &pluginv1.Payload{Bytes: args}}, istream); err != nil {
		t.Fatalf("create-vpc: %v", err)
	}
	var created map[string]any
	_ = json.Unmarshal(istream.sent[len(istream.sent)-1].GetResult().GetOutputs().GetBytes(), &created)
	vpcID := created["vpcId"].(string)
	if vpcID == "" {
		t.Fatal("create-vpc returned no vpcId")
	}
	t.Logf("LIVE created vpc=%s", vpcID)

	// Observe the account and find our VPC as a first-class Entity.
	ostream := &captureStream[pluginv1.ObserveResponse]{ctx: context.Background()}
	if err := srv.Observe(&pluginv1.ObserveRequest{}, ostream); err != nil {
		t.Fatalf("observe: %v", err)
	}
	kinds := map[string]int{}
	var vpcEnt *pluginv1.ObservedEntity
	for _, resp := range ostream.sent {
		for _, e := range resp.GetEntities() {
			kinds[e.GetKind()]++
			if e.GetKind() == "vpc" && e.GetIdentityKeys()["aws.vpcId"] == vpcID {
				vpcEnt = e
			}
		}
	}
	t.Logf("LIVE observed kinds: %v", kinds)
	if vpcEnt == nil {
		t.Fatalf("Observe did not project the created vpc %s as a vpc Entity", vpcID)
	}
	if vpcEnt.GetLabels()["stratt.managed"] != "true" {
		t.Errorf("the stratt-created vpc must carry the stratt.managed label, got %v", vpcEnt.GetLabels())
	}
	if len(vpcEnt.GetFacets()["net.vpc"]) == 0 {
		t.Error("observed vpc must carry a net.vpc facet")
	}
	// The resource-graph kinds all enumerate (subnets/sgs/volumes from earlier live tests).
	for _, k := range []string{"vpc", "subnet", "security-group", "volume"} {
		if kinds[k] == 0 {
			t.Errorf("expected at least one %q Entity from the live account", k)
		}
	}
}
