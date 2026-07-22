package awsec2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// TestNormalizedFacetsMatchContracts is the co-fidelity guard (ADR-0095 flag 2): the
// instance.* Facet schemas are CLOSED (additionalProperties:false) and the Syncer now
// writes live, so ANY field normalizeInstance emits that a schema omits flips from a
// silent pass into a BLOCKING write-path rejection (§1.5 drift is blocking). This test
// builds a maximally-populated instance and asserts every emitted Facet key is declared
// in the shipped schema — normalize.go and the contracts must move together forever.
func TestNormalizedFacetsMatchContracts(t *testing.T) {
	launch := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	full := ec2types.Instance{
		InstanceId:       aws.String("i-full"),
		InstanceType:     ec2types.InstanceTypeT3Micro,
		Architecture:     ec2types.ArchitectureValuesX8664,
		ImageId:          aws.String("ami-1"),
		PrivateIpAddress: aws.String("10.0.0.5"),
		PublicIpAddress:  aws.String("54.1.2.3"),
		VpcId:            aws.String("vpc-1"),
		SubnetId:         aws.String("subnet-1"),
		Placement:        &ec2types.Placement{AvailabilityZone: aws.String("us-east-1a")},
		State:            &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
		LaunchTime:       &launch,
		Tags:             []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("web-01")}},
	}
	e, err := normalizeInstance("us-east-1", full)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(e.GetFacets()) != 3 {
		t.Fatalf("a fully-populated instance must emit all 3 facets, got %v", facetKeys(e.GetFacets()))
	}
	for ns, raw := range e.GetFacets() {
		allowed := schemaProperties(t, ns)
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", ns, err)
		}
		if len(doc) == 0 {
			t.Fatalf("%s: fully-populated instance emitted an empty facet", ns)
		}
		for k := range doc {
			if !allowed[k] {
				t.Errorf("%s emits key %q not declared in its CLOSED schema — the live Syncer would REJECT this write (ADR-0095 flag 2)", ns, k)
			}
		}
	}
}

// TestNormalizedResourceFacetsMatchContracts extends the co-fidelity guard (ADR-0096
// flag 2) to the four resource kinds: each normalizer's emitted Facet keys must be a
// subset of its closed schema, so a live Syncer write is never rejected.
func TestNormalizedResourceFacetsMatchContracts(t *testing.T) {
	check := func(t *testing.T, ns string, facets map[string][]byte) {
		allowed := schemaProperties(t, ns)
		var doc map[string]any
		if err := json.Unmarshal(facets[ns], &doc); err != nil {
			t.Fatalf("%s: %v", ns, err)
		}
		if len(doc) == 0 {
			t.Fatalf("%s: fully-populated object emitted an empty facet", ns)
		}
		for k := range doc {
			if !allowed[k] {
				t.Errorf("%s emits key %q not in its CLOSED schema — the live Syncer would REJECT this write", ns, k)
			}
		}
	}
	t.Run("vpc", func(t *testing.T) {
		e := normalizeVPC("us-east-1", ec2types.Vpc{VpcId: aws.String("vpc-1"), CidrBlock: aws.String("10.0.0.0/16"), State: ec2types.VpcStateAvailable, IsDefault: aws.Bool(true)})
		check(t, "net.vpc", e.GetFacets())
	})
	t.Run("subnet", func(t *testing.T) {
		e := normalizeSubnet("us-east-1", ec2types.Subnet{SubnetId: aws.String("subnet-1"), VpcId: aws.String("vpc-1"), CidrBlock: aws.String("10.0.1.0/24"), AvailabilityZone: aws.String("us-east-1a"), State: ec2types.SubnetStateAvailable})
		check(t, "net.subnet", e.GetFacets())
		if len(e.GetRelations()) != 1 || e.GetRelations()[0].GetType() != "in-vpc" {
			t.Fatalf("subnet must carry an in-vpc relation: %+v", e.GetRelations())
		}
	})
	t.Run("security-group", func(t *testing.T) {
		e := normalizeSecurityGroup("us-east-1", ec2types.SecurityGroup{GroupId: aws.String("sg-1"), GroupName: aws.String("web"), Description: aws.String("web sg"), VpcId: aws.String("vpc-1")})
		check(t, "net.securitygroup", e.GetFacets())
	})
	t.Run("volume", func(t *testing.T) {
		e := normalizeVolume("us-east-1", ec2types.Volume{VolumeId: aws.String("vol-1"), Size: aws.Int32(8), VolumeType: ec2types.VolumeTypeGp3, State: ec2types.VolumeStateInUse, AvailabilityZone: aws.String("us-east-1a"), Attachments: []ec2types.VolumeAttachment{{InstanceId: aws.String("i-1")}}})
		check(t, "storage.volume", e.GetFacets())
		if len(e.GetRelations()) != 1 || e.GetRelations()[0].GetType() != "attached-to" {
			t.Fatalf("attached volume must carry an attached-to relation: %+v", e.GetRelations())
		}
	})
}

// TestStrattManagedLabel proves the anti-orphan marker (ADR-0095 flag 1) surfaces as a
// stratt.managed label on the observed Entity, so queries can enumerate what Stratt made.
func TestStrattManagedLabel(t *testing.T) {
	managed := normalizeVPC("us-east-1", ec2types.Vpc{VpcId: aws.String("vpc-m"), Tags: []ec2types.Tag{{Key: aws.String("stratt:managed"), Value: aws.String("true")}}})
	if managed.GetLabels()["stratt.managed"] != "true" {
		t.Fatalf("stratt:managed tag must surface as the stratt.managed label: %v", managed.GetLabels())
	}
	unmanaged := normalizeVPC("us-east-1", ec2types.Vpc{VpcId: aws.String("vpc-u")})
	if _, ok := unmanaged.GetLabels()["stratt.managed"]; ok {
		t.Fatal("an unmarked object must NOT carry the stratt.managed label")
	}
}

// schemaProperties reads the shipped facet schema for ns and returns its declared
// property names.
func schemaProperties(t *testing.T, ns string) map[string]bool {
	t.Helper()
	path := filepath.Join("..", "..", "contracts", "facets", ns+".schema.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read schema %s: %v", path, err)
	}
	var doc struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse schema %s: %v", path, err)
	}
	if doc.AdditionalProperties == nil || *doc.AdditionalProperties {
		t.Fatalf("%s schema must be CLOSED (additionalProperties:false) for the co-fidelity guarantee", ns)
	}
	out := map[string]bool{}
	for k := range doc.Properties {
		out[k] = true
	}
	return out
}
