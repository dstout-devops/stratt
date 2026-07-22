package awsec2

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// resourceLabels builds the common labels for an observed resource Entity (ADR-0096):
// aws.region always; aws.name from a Name tag; and stratt.managed=true when the object
// carries the stratt:managed marker this plugin stamps on what it creates (ADR-0095
// flag 1) — so a View/query can enumerate exactly what Stratt provisioned.
func resourceLabels(region string, tags []ec2types.Tag) map[string]string {
	labels := map[string]string{"aws.region": region}
	for _, t := range tags {
		switch aws.ToString(t.Key) {
		case "Name":
			if v := aws.ToString(t.Value); v != "" {
				labels["aws.name"] = v
			}
		case "stratt:managed":
			if aws.ToString(t.Value) == "true" {
				labels["stratt.managed"] = "true"
			}
		}
	}
	return labels
}

// facetBytes marshals a facet doc, dropping empty. Returns nil (skip) when the doc has
// no populated fields — the same emit-only-when-populated discipline as normalizeInstance.
func facetBytes(doc map[string]any) []byte {
	if len(doc) == 0 {
		return nil
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	return raw
}

// normalizeVPC maps an observed VPC to a `vpc` Entity (identity aws.vpcId, Facet net.vpc).
func normalizeVPC(region string, v ec2types.Vpc) *pluginv1.ObservedEntity {
	id := aws.ToString(v.VpcId)
	if id == "" {
		return nil
	}
	doc := map[string]any{"isDefault": aws.ToBool(v.IsDefault)}
	if c := aws.ToString(v.CidrBlock); c != "" {
		doc["cidr"] = c
	}
	if s := string(v.State); s != "" {
		doc["state"] = s
	}
	return &pluginv1.ObservedEntity{
		Kind:         "vpc",
		IdentityKeys: map[string]string{"aws.vpcId": id},
		Labels:       resourceLabels(region, v.Tags),
		Facets:       map[string][]byte{"net.vpc": facetBytes(doc)},
	}
}

// normalizeSubnet maps an observed subnet to the SHARED `subnet` Entity (identity
// aws.subnetId, Facet net.subnet — co-owned with crossplane/NetBox, ADR-0060) with an
// in-vpc Relation.
func normalizeSubnet(region string, s ec2types.Subnet) *pluginv1.ObservedEntity {
	id := aws.ToString(s.SubnetId)
	if id == "" {
		return nil
	}
	doc := map[string]any{}
	if c := aws.ToString(s.CidrBlock); c != "" {
		doc["cidr"] = c
	}
	if az := aws.ToString(s.AvailabilityZone); az != "" {
		doc["availabilityZone"] = az
	}
	if st := string(s.State); st != "" {
		doc["state"] = st
	}
	var rels []*pluginv1.ObservedRelation
	if vpc := aws.ToString(s.VpcId); vpc != "" {
		doc["vpcId"] = vpc
		rels = append(rels, &pluginv1.ObservedRelation{Type: "in-vpc", ToScheme: "aws.vpcId", ToValue: vpc})
	}
	return &pluginv1.ObservedEntity{
		Kind:         "subnet",
		IdentityKeys: map[string]string{"aws.subnetId": id},
		Labels:       resourceLabels(region, s.Tags),
		Facets:       map[string][]byte{"net.subnet": facetBytes(doc)},
		Relations:    rels,
	}
}

// normalizeSecurityGroup maps an observed security group to a `security-group` Entity
// (identity aws.securityGroupId, Facet net.securitygroup) with an in-vpc Relation.
func normalizeSecurityGroup(region string, g ec2types.SecurityGroup) *pluginv1.ObservedEntity {
	id := aws.ToString(g.GroupId)
	if id == "" {
		return nil
	}
	doc := map[string]any{}
	if n := aws.ToString(g.GroupName); n != "" {
		doc["groupName"] = n
	}
	if d := aws.ToString(g.Description); d != "" {
		doc["description"] = d
	}
	var rels []*pluginv1.ObservedRelation
	if vpc := aws.ToString(g.VpcId); vpc != "" {
		doc["vpcId"] = vpc
		rels = append(rels, &pluginv1.ObservedRelation{Type: "in-vpc", ToScheme: "aws.vpcId", ToValue: vpc})
	}
	return &pluginv1.ObservedEntity{
		Kind:         "security-group",
		IdentityKeys: map[string]string{"aws.securityGroupId": id},
		Labels:       resourceLabels(region, g.Tags),
		Facets:       map[string][]byte{"net.securitygroup": facetBytes(doc)},
		Relations:    rels,
	}
}

// normalizeVolume maps an observed volume to a `volume` Entity (identity aws.volumeId,
// Facet storage.volume) with an attached-to Relation when mounted on an instance.
func normalizeVolume(region string, v ec2types.Volume) *pluginv1.ObservedEntity {
	id := aws.ToString(v.VolumeId)
	if id == "" {
		return nil
	}
	doc := map[string]any{}
	if sz := aws.ToInt32(v.Size); sz > 0 {
		doc["sizeGiB"] = sz
	}
	if vt := string(v.VolumeType); vt != "" {
		doc["volumeType"] = vt
	}
	if st := string(v.State); st != "" {
		doc["state"] = st
	}
	if az := aws.ToString(v.AvailabilityZone); az != "" {
		doc["availabilityZone"] = az
	}
	var rels []*pluginv1.ObservedRelation
	for _, a := range v.Attachments {
		if inst := aws.ToString(a.InstanceId); inst != "" {
			rels = append(rels, &pluginv1.ObservedRelation{Type: "attached-to", ToScheme: "aws.instanceId", ToValue: inst})
		}
	}
	return &pluginv1.ObservedEntity{
		Kind:         "volume",
		IdentityKeys: map[string]string{"aws.volumeId": id},
		Labels:       resourceLabels(region, v.Tags),
		Facets:       map[string][]byte{"storage.volume": facetBytes(doc)},
		Relations:    rels,
	}
}

// observeVPCs / Subnets / SecurityGroups / Volumes each paginate the matching Describe
// call and normalize every object — full enumeration for the Syncer full-sync (ADR-0096).

func observeVPCs(ctx context.Context, api EC2API, region string) ([]*pluginv1.ObservedEntity, error) {
	var out []*pluginv1.ObservedEntity
	pager := ec2.NewDescribeVpcsPaginator(api, &ec2.DescribeVpcsInput{})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("awsec2: describe vpcs: %w", err)
		}
		for _, v := range page.Vpcs {
			if e := normalizeVPC(region, v); e != nil {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

func observeSubnets(ctx context.Context, api EC2API, region string) ([]*pluginv1.ObservedEntity, error) {
	var out []*pluginv1.ObservedEntity
	pager := ec2.NewDescribeSubnetsPaginator(api, &ec2.DescribeSubnetsInput{})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("awsec2: describe subnets: %w", err)
		}
		for _, s := range page.Subnets {
			if e := normalizeSubnet(region, s); e != nil {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

func observeSecurityGroups(ctx context.Context, api EC2API, region string) ([]*pluginv1.ObservedEntity, error) {
	var out []*pluginv1.ObservedEntity
	pager := ec2.NewDescribeSecurityGroupsPaginator(api, &ec2.DescribeSecurityGroupsInput{})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("awsec2: describe security groups: %w", err)
		}
		for _, g := range page.SecurityGroups {
			if e := normalizeSecurityGroup(region, g); e != nil {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

func observeVolumes(ctx context.Context, api EC2API, region string) ([]*pluginv1.ObservedEntity, error) {
	var out []*pluginv1.ObservedEntity
	pager := ec2.NewDescribeVolumesPaginator(api, &ec2.DescribeVolumesInput{})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("awsec2: describe volumes: %w", err)
		}
		for _, v := range page.Volumes {
			if e := normalizeVolume(region, v); e != nil {
				out = append(out, e)
			}
		}
	}
	return out, nil
}
