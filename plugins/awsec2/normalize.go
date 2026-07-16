// Package awsec2 is the AWS EC2 Connector plugin: the aws-sdk-go-v2
// content-expertise that used to live in core/internal/connectors/awsec2 (the
// Syncer) and core/internal/actions/awsec2 (the create-vm Action), now behind the
// sovereign plugin port (ADR-0046). It maps EC2 objects to core-legible
// ObservedEntity wire values; the core-side host governs what it may write
// (ownership, identity gating, provenance). The plugin holds no graph write path.
package awsec2

import (
	"encoding/json"
	"fmt"

	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// normalizeInstance maps one observed EC2 instance onto an ObservedEntity — the
// minimum demanded by the shipped Facet namespaces (§1.1). Pure content-expertise;
// the plugin only proposes, it never writes (§1.2). Identity is aws.instanceId;
// labels are aws.region + (when tagged) aws.name; facets are instance.compute /
// instance.network / instance.state, each emitted only when populated.
func normalizeInstance(region string, in ec2types.Instance) (*pluginv1.ObservedEntity, error) {
	if in.InstanceId == nil || *in.InstanceId == "" {
		return nil, fmt.Errorf("awsec2: instance without an id; cannot project without identity")
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

	facets := map[string][]byte{}
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
			return nil, fmt.Errorf("awsec2: marshal facet %s: %w", ns, err)
		}
		facets[ns] = raw
	}

	return &pluginv1.ObservedEntity{
		Kind:         "instance",
		IdentityKeys: identity,
		Labels:       labels,
		Facets:       facets,
	}, nil
}
