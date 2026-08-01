package awsec2

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/dstout-devops/stratt/sdk/mockstratt"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// ADR-0143 applied to EC2: a NAME first, and never the public one.
func TestReachCoordinate(t *testing.T) {
	cases := []struct {
		name string
		in   ec2types.Instance
		want string
	}{
		{
			name: "the private DNS name wins over the private address",
			in: ec2types.Instance{
				PrivateDnsName:   aws.String("ip-10-30-1-7.ec2.internal"),
				PrivateIpAddress: aws.String("10.30.1.7"),
			},
			want: "ip-10-30-1-7.ec2.internal",
		},
		{
			name: "lowercased — DNS is case-insensitive, the graph should not be",
			in:   ec2types.Instance{PrivateDnsName: aws.String("IP-10-30-1-7.EC2.Internal")},
			want: "ip-10-30-1-7.ec2.internal",
		},
		{
			// The substrate-specific judgement. An instance that merely happens to have a
			// public interface must not become managed over the internet by default.
			name: "PUBLIC addressing is never a fallback",
			in: ec2types.Instance{
				PublicDnsName:    aws.String("ec2-203-0-113-9.compute-1.amazonaws.com"),
				PublicIpAddress:  aws.String("203.0.113.9"),
				PrivateIpAddress: aws.String("10.30.1.7"),
			},
			want: "10.30.1.7",
		},
		{
			name: "public only yields NOTHING, not a public coordinate",
			in: ec2types.Instance{
				PublicDnsName:   aws.String("ec2-203-0-113-9.compute-1.amazonaws.com"),
				PublicIpAddress: aws.String("203.0.113.9"),
			},
			want: "",
		},
		{
			name: "private address only",
			in:   ec2types.Instance{PrivateIpAddress: aws.String("10.30.1.7")},
			want: "10.30.1.7",
		},
		{
			// "Built, not yet reachable" — a pending instance. Honest absence beats a guess.
			name: "nothing known yields no coordinate",
			in:   ec2types.Instance{},
			want: "",
		},
		{
			name: "whitespace is not a coordinate",
			in:   ec2types.Instance{PrivateDnsName: aws.String("  "), PrivateIpAddress: aws.String(" ")},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reachCoordinate(tc.in); got != tc.want {
				t.Errorf("reachCoordinate = %q, want %q", got, tc.want)
			}
		})
	}
}

// The coordinate must reach the projection, and the FACT behind it must be projected
// too — otherwise mgmt.address is a name appearing nowhere else in the graph.
func TestNormalizeInstanceProjectsMgmtAddress(t *testing.T) {
	got, err := normalizeInstance("us-east-1", ec2types.Instance{
		InstanceId:       aws.String("i-abc"),
		PrivateDnsName:   aws.String("ip-10-30-1-7.ec2.internal"),
		PrivateIpAddress: aws.String("10.30.1.7"),
	})
	if err != nil {
		t.Fatalf("normalizeInstance: %v", err)
	}
	var facet map[string]any
	if err := json.Unmarshal(got.Facets["mgmt.address"], &facet); err != nil {
		t.Fatalf("mgmt.address absent or invalid: %v", err)
	}
	if facet["address"] != "ip-10-30-1-7.ec2.internal" {
		t.Errorf("address = %v, want the private DNS name", facet["address"])
	}
	// The mgmt.address schema is CLOSED (§9) — anything else fails host validation.
	for k := range facet {
		if k != "address" && k != "port" {
			t.Errorf("mgmt.address carries %q; the schema is closed to {address, port}", k)
		}
	}
	var network map[string]any
	if err := json.Unmarshal(got.Facets["instance.network"], &network); err != nil {
		t.Fatalf("instance.network: %v", err)
	}
	if network["privateDnsName"] != "ip-10-30-1-7.ec2.internal" {
		t.Errorf("the name the coordinate resolves to must be projected as a fact, for auditability (§1.8); got %v", network)
	}
}

func TestNormalizeInstanceOmitsMgmtAddressWhenUnknown(t *testing.T) {
	got, err := normalizeInstance("us-east-1", ec2types.Instance{InstanceId: aws.String("i-pending")})
	if err != nil {
		t.Fatalf("normalizeInstance: %v", err)
	}
	if _, ok := got.Facets["mgmt.address"]; ok {
		t.Error("a pending instance has no known coordinate; absence is the honest projection")
	}
}

// Port conformance for the awsec2 Syncer — the Observe suite's second subject, and the
// guard that keeps this plugin's advertisement in step with what it projects. The
// namespace under test is exercised deliberately rather than left to a fixture, for the
// reason vcenter's equivalent records: a sweep that never produces the namespace passes
// for the wrong reason.
func TestObserveConformance(t *testing.T) {
	e, err := normalizeInstance("us-east-1", ec2types.Instance{
		InstanceId:     aws.String("i-abc"),
		PrivateDnsName: aws.String("ip-10-30-1-7.ec2.internal"),
		InstanceType:   ec2types.InstanceTypeT3Micro,
		State:          &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
	})
	if err != nil {
		t.Fatalf("normalizeInstance: %v", err)
	}
	if len(e.GetFacets()["mgmt.address"]) == 0 {
		t.Fatal("fixture does not exercise mgmt.address; this test would be vacuous")
	}
	mres, err := (&Server{}).GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	conf := mockstratt.ObserveConformance{
		Result: mockstratt.ObserveResult{
			Entities: []mockstratt.Entity{{
				Kind: e.GetKind(), IdentityKeys: e.GetIdentityKeys(),
				Labels: e.GetLabels(), Facets: e.GetFacets(),
			}},
			FullSyncComplete: true,
		},
		Manifest: mres.GetManifest(),
	}
	if errs := conf.Errors(); len(errs) > 0 {
		t.Fatalf("awsec2 violates port conformance:\n%s", conf.Report())
	}
}
