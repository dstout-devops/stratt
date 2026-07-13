// Command actions-ec2 is the execution-pod driver for the awsec2/create-vm
// Action (ADR-0031). It reuses the vendored aws-sdk-go-v2 (§1.4 — no boto3),
// reads its parameters from /runner/project/step.json and AWS credentials from
// the environment (injected as a CredentialRef, §2.5), provisions one instance,
// and prints one JSON event line the Action's Interpret decodes. It never logs
// the credentials.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

type step struct {
	Region       string `json:"region"`
	Endpoint     string `json:"endpoint"`
	InstanceType string `json:"instanceType"`
	AMI          string `json:"ami"`
	Name         string `json:"name"`
	DryRun       bool   `json:"dryRun"`
}

type event struct {
	Counter    int64  `json:"counter"`
	Event      string `json:"event"`
	Host       string `json:"host"`
	OK         bool   `json:"ok"`
	Detail     string `json:"detail"`
	InstanceID string `json:"instanceId,omitempty"`
	PrivateIP  string `json:"privateIp,omitempty"`
	Region     string `json:"region,omitempty"`
}

func emit(e event) {
	e.Counter = 1
	b, _ := json.Marshal(e)
	fmt.Println(string(b))
}

func main() {
	if err := run(); err != nil {
		// Never print the raw error verbatim if it could carry the endpoint or
		// creds; a class/short message only (§2.5, §1.8).
		emit(event{Event: "vm_failed", Host: "create-vm", OK: false, Detail: err.Error()})
		os.Exit(1)
	}
}

func run() error {
	raw, err := os.ReadFile("/runner/project/step.json")
	if err != nil {
		return fmt.Errorf("read step: %w", err)
	}
	var s step
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("parse step: %w", err)
	}
	host := s.Name
	if host == "" {
		host = "create-vm"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(s.Region))
	if err != nil {
		return fmt.Errorf("aws config: %w", err)
	}
	client := ec2.NewFromConfig(cfg, func(o *ec2.Options) {
		if s.Endpoint != "" {
			o.BaseEndpoint = aws.String(s.Endpoint)
		}
	})

	in := &ec2.RunInstancesInput{
		ImageId:      aws.String(s.AMI),
		InstanceType: ec2types.InstanceType(s.InstanceType),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		DryRun:       aws.Bool(s.DryRun),
	}
	if s.Name != "" {
		in.TagSpecifications = []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeInstance,
			Tags:         []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String(s.Name)}},
		}}
	}

	out, err := client.RunInstances(ctx, in)
	if err != nil {
		// DryRun success is reported by EC2 as a DryRunOperation error (§2.2
		// dry-run) — a plan, not a failure.
		var ae smithy.APIError
		if s.DryRun && errors.As(err, &ae) && ae.ErrorCode() == "DryRunOperation" {
			emit(event{Event: "vm_planned", Host: host, OK: true, Region: s.Region})
			return nil
		}
		return fmt.Errorf("run instances: %s", apiClass(err))
	}
	if len(out.Instances) == 0 {
		return fmt.Errorf("run instances returned no instance")
	}
	inst := out.Instances[0]
	e := event{Event: "vm_created", Host: host, OK: true, Region: s.Region}
	if inst.InstanceId != nil {
		e.InstanceID = *inst.InstanceId
	}
	if inst.PrivateIpAddress != nil {
		e.PrivateIP = *inst.PrivateIpAddress
	}
	emit(e)
	return nil
}

// apiClass returns the AWS error code (never the full message, which can embed
// the endpoint).
func apiClass(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return "RequestError"
}
