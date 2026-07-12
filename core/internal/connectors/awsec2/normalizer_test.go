package awsec2

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func TestNormalizeInstance(t *testing.T) {
	launch := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	up, err := normalizeInstance("us-east-1", ec2types.Instance{
		InstanceId:       aws.String("i-0abc"),
		InstanceType:     ec2types.InstanceTypeT3Micro,
		Architecture:     ec2types.ArchitectureValuesX8664,
		ImageId:          aws.String("ami-123"),
		PrivateIpAddress: aws.String("10.0.0.5"),
		VpcId:            aws.String("vpc-1"),
		SubnetId:         aws.String("subnet-1"),
		Placement:        &ec2types.Placement{AvailabilityZone: aws.String("us-east-1a")},
		State:            &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
		LaunchTime:       &launch,
		Tags:             []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("web-01")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if up.Kind != "instance" || up.IdentityKeys["aws.instanceId"] != "i-0abc" {
		t.Fatalf("upsert: %+v", up)
	}
	if up.Labels["aws.name"] != "web-01" || up.Labels["aws.region"] != "us-east-1" {
		t.Fatalf("labels: %+v", up.Labels)
	}
	var compute, network, state map[string]any
	if err := json.Unmarshal(up.Facets["instance.compute"], &compute); err != nil || compute["instanceType"] != "t3.micro" {
		t.Fatalf("instance.compute: %s %v", up.Facets["instance.compute"], err)
	}
	if err := json.Unmarshal(up.Facets["instance.network"], &network); err != nil || network["availabilityZone"] != "us-east-1a" {
		t.Fatalf("instance.network: %s %v", up.Facets["instance.network"], err)
	}
	if err := json.Unmarshal(up.Facets["instance.state"], &state); err != nil || state["state"] != "running" {
		t.Fatalf("instance.state: %s %v", up.Facets["instance.state"], err)
	}

	// No identity → refuse to project (§1.2).
	if _, err := normalizeInstance("us-east-1", ec2types.Instance{}); err == nil {
		t.Fatal("instance without id must be rejected")
	}

	// Sparse instance → only populated facets.
	sparse, err := normalizeInstance("us-east-1", ec2types.Instance{InstanceId: aws.String("i-1")})
	if err != nil {
		t.Fatal(err)
	}
	if len(sparse.Facets) != 0 {
		t.Fatalf("sparse instance should carry no empty facets: %v", sparse.Facets)
	}
}
