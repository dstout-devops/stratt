package awsec2

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// hasStrattMarker reports whether the tag specs carry the stratt:managed=true marker
// (ADR-0095 flag 1 — the anti-orphan tag the C3 Syncer / an orphan scan keys on).
func hasStrattMarker(specs []ec2types.TagSpecification) bool {
	for _, s := range specs {
		for _, t := range s.Tags {
			if aws.ToString(t.Key) == "stratt:managed" && aws.ToString(t.Value) == "true" {
				return true
			}
		}
	}
	return false
}

func runResource(t *testing.T, api EC2API, action string, args any) *pluginv1.InvokeResult {
	t.Helper()
	raw, _ := json.Marshal(args)
	stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
	if err := newServer(t, api).Invoke(&pluginv1.InvokeRequest{Action: action, Args: &pluginv1.Payload{Bytes: raw}}, stream); err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	term := stream.sent[len(stream.sent)-1]
	if !term.GetEvent().GetTerminal() || !term.GetEvent().GetOk() {
		t.Fatalf("%s not terminal-ok: %q", action, term.GetEvent().GetMessage())
	}
	// Fire-and-return: no Entity projected (C3 will model these), and the per-op
	// output contract is stamped.
	if len(term.GetResult().GetEntities()) != 0 {
		t.Fatalf("%s must be fire-and-return (no Entity), got %d", action, len(term.GetResult().GetEntities()))
	}
	op := action[len("awsec2/"):]
	if term.GetResult().GetOutputContract().GetSchemaId() != "actions/awsec2/"+op+".output" {
		t.Errorf("%s output contract: %q", action, term.GetResult().GetOutputContract().GetSchemaId())
	}
	return term.GetResult()
}

func outMap(t *testing.T, res *pluginv1.InvokeResult) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(res.GetOutputs().GetBytes(), &m); err != nil {
		t.Fatalf("outputs: %v", err)
	}
	return m
}

// TestInvokeCreateResources proves each fire-and-return resource Action: it calls the
// right EC2 op, stamps the stratt-owned marker tag (flag 1), and returns the per-op
// typed id (flag 4 — never a generic resourceId).
func TestInvokeCreateResources(t *testing.T) {
	t.Run("security-group", func(t *testing.T) {
		api := &fakeEC2{sgOut: &ec2.CreateSecurityGroupOutput{GroupId: aws.String("sg-1")}}
		res := runResource(t, api, "awsec2/create-security-group", map[string]any{"groupName": "web", "description": "web sg", "vpcId": "vpc-1"})
		if outMap(t, res)["securityGroupId"] != "sg-1" {
			t.Fatalf("securityGroupId: %v", outMap(t, res))
		}
		if !hasStrattMarker(api.lastSG.TagSpecifications) {
			t.Error("security group must carry the stratt:managed marker tag")
		}
		if aws.ToString(api.lastSG.VpcId) != "vpc-1" {
			t.Errorf("vpcId not threaded: %v", api.lastSG.VpcId)
		}
	})

	t.Run("import-key-pair", func(t *testing.T) {
		api := &fakeEC2{keyOut: &ec2.ImportKeyPairOutput{KeyName: aws.String("k1"), KeyPairId: aws.String("key-1")}}
		res := runResource(t, api, "awsec2/import-key-pair", map[string]any{"keyName": "k1", "publicKey": "ssh-ed25519 AAAA..."})
		if outMap(t, res)["keyPairId"] != "key-1" {
			t.Fatalf("keyPairId: %v", outMap(t, res))
		}
		// §2.5: the PUBLIC key crosses (ImportKeyPair), never a generated private key.
		if string(api.lastKey.PublicKeyMaterial) != "ssh-ed25519 AAAA..." {
			t.Errorf("public key not passed to ImportKeyPair: %q", api.lastKey.PublicKeyMaterial)
		}
		if !hasStrattMarker(api.lastKey.TagSpecifications) {
			t.Error("key pair must carry the stratt:managed marker tag")
		}
	})

	t.Run("volume", func(t *testing.T) {
		api := &fakeEC2{volOut: &ec2.CreateVolumeOutput{VolumeId: aws.String("vol-1")}}
		res := runResource(t, api, "awsec2/create-volume", map[string]any{"availabilityZone": "us-east-1a", "sizeGiB": 8, "volumeType": "gp3"})
		if outMap(t, res)["volumeId"] != "vol-1" {
			t.Fatalf("volumeId: %v", outMap(t, res))
		}
		if aws.ToInt32(api.lastVol.Size) != 8 || api.lastVol.VolumeType != ec2types.VolumeTypeGp3 {
			t.Errorf("volume input: size=%d type=%s", aws.ToInt32(api.lastVol.Size), api.lastVol.VolumeType)
		}
		if !hasStrattMarker(api.lastVol.TagSpecifications) {
			t.Error("volume must carry the stratt:managed marker tag")
		}
	})

	t.Run("vpc", func(t *testing.T) {
		api := &fakeEC2{vpcOut: &ec2.CreateVpcOutput{Vpc: &ec2types.Vpc{VpcId: aws.String("vpc-9")}}}
		res := runResource(t, api, "awsec2/create-vpc", map[string]any{"cidrBlock": "10.0.0.0/16"})
		if outMap(t, res)["vpcId"] != "vpc-9" {
			t.Fatalf("vpcId: %v", outMap(t, res))
		}
		if !hasStrattMarker(api.lastVpc.TagSpecifications) {
			t.Error("vpc must carry the stratt:managed marker tag")
		}
	})

	t.Run("subnet", func(t *testing.T) {
		api := &fakeEC2{subnetOut: &ec2.CreateSubnetOutput{Subnet: &ec2types.Subnet{SubnetId: aws.String("subnet-9")}}}
		res := runResource(t, api, "awsec2/create-subnet", map[string]any{"vpcId": "vpc-9", "cidrBlock": "10.0.1.0/24"})
		if outMap(t, res)["subnetId"] != "subnet-9" {
			t.Fatalf("subnetId: %v", outMap(t, res))
		}
		if aws.ToString(api.lastSubnet.VpcId) != "vpc-9" || !hasStrattMarker(api.lastSubnet.TagSpecifications) {
			t.Error("subnet must thread vpcId + carry the stratt:managed marker tag")
		}
	})
}

// TestCreateResourceValidation — required fields are enforced per resource.
func TestCreateResourceValidation(t *testing.T) {
	for _, tc := range []struct {
		action string
		args   map[string]any
	}{
		{"awsec2/create-security-group", map[string]any{"groupName": "x"}}, // missing description
		{"awsec2/import-key-pair", map[string]any{"keyName": "k"}},         // missing publicKey
		{"awsec2/create-volume", map[string]any{"availabilityZone": "z"}},  // missing sizeGiB
		{"awsec2/create-vpc", map[string]any{}},                            // missing cidrBlock
		{"awsec2/create-subnet", map[string]any{"vpcId": "v"}},             // missing cidrBlock
	} {
		raw, _ := json.Marshal(tc.args)
		stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
		if err := newServer(t, &fakeEC2{}).Invoke(&pluginv1.InvokeRequest{Action: tc.action, Args: &pluginv1.Payload{Bytes: raw}}, stream); err == nil {
			t.Errorf("%s with missing required field must be rejected", tc.action)
		}
	}
}
