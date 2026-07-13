// Package awsec2 ships the create-vm Action of the awsec2 Connector (charter
// §2.2 headline example, ADR-0031): provision one EC2 instance as a single
// typed operation. It is the read-side awsec2 Syncer's write-side sibling — the
// Connector now ships Syncer + Action.
//
// The driver is a small Go binary (cmd/actions-ec2) reusing the vendored
// aws-sdk-go-v2 (no boto3; §1.4 tiny static binary) in the stratt-ee-actions
// image. It provisions the instance and emits one terminal event carrying the
// typed outputs AND an Entity observation, so the new VM projects into the graph
// with Run provenance (§1.2 — the ADR-0017 stratt_entities path, Action-typed).
// AWS credentials inject as a CredentialRef into the pod at spawn (§2.5).
package awsec2

import (
	"encoding/json"
	"fmt"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/types"
)

// Action provisions one EC2 instance. image is the EE image carrying the Go
// driver (JobSpec.Image override).
type Action struct{ image string }

// CreateVM builds the create-vm Action for the given EE image.
func CreateVM(image string) Action { return Action{image: image} }

// Name implements actions.Action.
func (Action) Name() string { return "awsec2/create-vm" }

// Idempotent implements actions.Action — each call provisions a new instance.
func (Action) Idempotent() bool { return false }

// DryRunnable implements actions.Action — RunInstances supports DryRun.
func (Action) DryRunnable() bool { return true }

// params is the input Contract (actions/awsec2/create-vm.input). AWS creds are
// NOT here — injected as a CredentialRef (§2.5).
type params struct {
	Region       string `json:"region"`
	Endpoint     string `json:"endpoint"`
	InstanceType string `json:"instanceType"`
	AMI          string `json:"ami"`
	Name         string `json:"name"`
}

// Prepare renders the Go driver invocation into pod content. dryRun asks EC2
// for a DryRun (no instance created).
func (a Action) Prepare(raw json.RawMessage, dryRun bool) (actuators.JobSpec, error) {
	var p params
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return actuators.JobSpec{}, fmt.Errorf("awsec2/create-vm: invalid params: %w", err)
		}
	}
	if p.Region == "" || p.AMI == "" {
		return actuators.JobSpec{}, fmt.Errorf("awsec2/create-vm requires region and ami")
	}
	if p.InstanceType == "" {
		p.InstanceType = "t3.micro"
	}
	step, err := json.Marshal(map[string]any{
		"region": p.Region, "endpoint": p.Endpoint, "instanceType": p.InstanceType,
		"ami": p.AMI, "name": p.Name, "dryRun": dryRun,
	})
	if err != nil {
		return actuators.JobSpec{}, err
	}
	return actuators.JobSpec{
		Image:   a.image,
		Files:   map[string]string{"project/step.json": string(step)},
		Command: []string{"/actions-ec2"},
	}, nil
}

// driverEvent is the one line the Go driver emits.
type driverEvent struct {
	Counter    int64  `json:"counter"`
	Event      string `json:"event"`
	Host       string `json:"host"`
	OK         bool   `json:"ok"`
	Detail     string `json:"detail"`
	InstanceID string `json:"instanceId"`
	PrivateIP  string `json:"privateIp"`
	Region     string `json:"region"`
}

// Interpret maps the driver event to a task event, the typed outputs, and an
// Entity observation for the provisioned instance (§1.2).
func (Action) Interpret(line []byte) (actuators.Interpreted, bool) {
	var ev driverEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Event == "" {
		return actuators.Interpreted{}, false
	}
	payload := map[string]any{}
	if ev.Detail != "" {
		payload["detail"] = ev.Detail
	}
	if ev.InstanceID != "" {
		payload["instanceId"] = ev.InstanceID
	}
	out := actuators.Interpreted{
		Event: types.RunEvent{Seq: ev.Counter, Kind: ev.Event, Target: ev.Host, Payload: payload},
	}
	if ev.OK && ev.InstanceID != "" && ev.Event != "vm_planned" {
		outputs, _ := json.Marshal(map[string]any{"instanceId": ev.InstanceID, "privateIp": ev.PrivateIP})
		out.Outputs = outputs
		// Project the new instance with Run provenance (§1.2). Facets arrive
		// from the awsec2 Syncer's next poll — the Action declares identity.
		out.Entities = []actuators.EntityObservation{{
			Kind:         "instance",
			IdentityKeys: map[string]string{"aws.instanceId": ev.InstanceID},
			Labels:       map[string]string{"aws.region": ev.Region},
		}}
	}
	status := actuators.StatusChanged
	if !ev.OK {
		status = actuators.StatusFailed
	}
	out.Result = &actuators.TargetResult{Target: ev.Host, Status: status, Failed: !ev.OK}
	return out, true
}
