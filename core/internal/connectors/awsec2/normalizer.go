package awsec2

import (
	"encoding/json"
	"fmt"

	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/dstout-devops/stratt/core/internal/graph"
)

// normalizeInstance maps one observed EC2 instance onto the graph shape —
// the minimum demanded by the shipped Facet namespaces (§1.1). Pure
// function — all writes go through the Projector in the Syncer.
func normalizeInstance(region string, in ec2types.Instance) (up graph.EntityUpsert, err error) {
	if in.InstanceId == nil || *in.InstanceId == "" {
		return up, fmt.Errorf("awsec2: instance without an id; cannot project without identity")
	}
	identity := map[string]string{"aws.instanceId": *in.InstanceId}
	labels := map[string]string{"aws.region": region}
	for _, t := range in.Tags {
		if t.Key != nil && *t.Key == "Name" && t.Value != nil && *t.Value != "" {
			labels["aws.name"] = *t.Value
		}
	}

	compute := map[string]any{}
	if in.InstanceType != "" {
		compute["instanceType"] = string(in.InstanceType)
	}
	if in.Architecture != "" {
		compute["architecture"] = string(in.Architecture)
	}
	if in.ImageId != nil && *in.ImageId != "" {
		compute["imageId"] = *in.ImageId
	}
	network := map[string]any{}
	if in.PrivateIpAddress != nil && *in.PrivateIpAddress != "" {
		network["privateIp"] = *in.PrivateIpAddress
	}
	if in.PublicIpAddress != nil && *in.PublicIpAddress != "" {
		network["publicIp"] = *in.PublicIpAddress
	}
	if in.VpcId != nil && *in.VpcId != "" {
		network["vpcId"] = *in.VpcId
	}
	if in.SubnetId != nil && *in.SubnetId != "" {
		network["subnetId"] = *in.SubnetId
	}
	if in.Placement != nil && in.Placement.AvailabilityZone != nil {
		network["availabilityZone"] = *in.Placement.AvailabilityZone
	}
	state := map[string]any{}
	if in.State != nil && in.State.Name != "" {
		state["state"] = string(in.State.Name)
	}
	if in.LaunchTime != nil {
		state["launchTime"] = in.LaunchTime.UTC()
	}

	facets := map[string]json.RawMessage{}
	for ns, doc := range map[string]map[string]any{
		"instance.compute": compute,
		"instance.network": network,
		"instance.state":   state,
	} {
		if len(doc) == 0 {
			continue
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return up, fmt.Errorf("awsec2: marshal facet %s: %w", ns, err)
		}
		facets[ns] = raw
	}

	return graph.EntityUpsert{
		Kind:         "instance",
		IdentityKeys: identity,
		Labels:       labels,
		Facets:       facets,
	}, nil
}
