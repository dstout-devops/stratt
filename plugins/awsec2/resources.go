package awsec2

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// strattTagSpec returns the stratt-owned marker TagSpecification stamped on every
// resource this plugin creates (ADR-0095 flag 1): stratt:managed=true + the invoke
// correlation id, so the C3 Syncer — or an interim orphan scan — can always enumerate
// what the platform provisioned. No silent billable leak.
func strattTagSpec(rt ec2types.ResourceType, correlationID string) []ec2types.TagSpecification {
	tags := []ec2types.Tag{{Key: aws.String("stratt:managed"), Value: aws.String("true")}}
	if correlationID != "" {
		tags = append(tags, ec2types.Tag{Key: aws.String("stratt:correlation"), Value: aws.String(correlationID)})
	}
	return []ec2types.TagSpecification{{ResourceType: rt, Tags: tags}}
}

// invokeCreateResource provisions one EC2 resource (security-group / key-pair / volume
// / vpc / subnet) fire-and-return: the object is created with the stratt-owned marker
// tag and its typed id returned as bindable output — NOT projected as an Observed Entity
// (that is C3, ADR-0096). §2.5: import-key-pair takes a PUBLIC key; no private material
// is ever generated server-side or returned through the core.
func (s *Server) invokeCreateResource(ctx context.Context, req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], api EC2API) error {
	action := req.GetAction()
	op := action[len("awsec2/"):]
	corr := req.GetEnvelope().GetCorrelationId()
	dry := aws.Bool(req.GetDryRun())

	if err := s.progress(stream, req, "provisioning "+op); err != nil {
		return err
	}

	var outputs map[string]any
	var err error
	switch action {
	case actionCreateSG:
		var p struct {
			GroupName   string `json:"groupName"`
			Description string `json:"description"`
			VpcID       string `json:"vpcId"`
		}
		if e := decodeArgs(req, &p); e != nil {
			return e
		}
		if p.GroupName == "" || p.Description == "" {
			return status.Errorf(codes.InvalidArgument, "%s requires groupName and description", action)
		}
		in := &ec2.CreateSecurityGroupInput{
			GroupName:         aws.String(p.GroupName),
			Description:       aws.String(p.Description),
			DryRun:            dry,
			TagSpecifications: strattTagSpec(ec2types.ResourceTypeSecurityGroup, corr),
		}
		if p.VpcID != "" {
			in.VpcId = aws.String(p.VpcID)
		}
		var out *ec2.CreateSecurityGroupOutput
		if out, err = api.CreateSecurityGroup(ctx, in); err == nil {
			outputs = map[string]any{"securityGroupId": aws.ToString(out.GroupId)}
		}
	case actionImportKey:
		var p struct {
			KeyName   string `json:"keyName"`
			PublicKey string `json:"publicKey"`
		}
		if e := decodeArgs(req, &p); e != nil {
			return e
		}
		if p.KeyName == "" || p.PublicKey == "" {
			return status.Errorf(codes.InvalidArgument, "%s requires keyName and publicKey (a PUBLIC key — never a private key, §2.5)", action)
		}
		in := &ec2.ImportKeyPairInput{
			KeyName:           aws.String(p.KeyName),
			PublicKeyMaterial: []byte(p.PublicKey),
			DryRun:            dry,
			TagSpecifications: strattTagSpec(ec2types.ResourceTypeKeyPair, corr),
		}
		var out *ec2.ImportKeyPairOutput
		if out, err = api.ImportKeyPair(ctx, in); err == nil {
			outputs = map[string]any{"keyName": aws.ToString(out.KeyName), "keyPairId": aws.ToString(out.KeyPairId)}
		}
	case actionCreateVolume:
		var p struct {
			AvailabilityZone string `json:"availabilityZone"`
			SizeGiB          int32  `json:"sizeGiB"`
			VolumeType       string `json:"volumeType"`
		}
		if e := decodeArgs(req, &p); e != nil {
			return e
		}
		if p.AvailabilityZone == "" || p.SizeGiB <= 0 {
			return status.Errorf(codes.InvalidArgument, "%s requires availabilityZone and a positive sizeGiB", action)
		}
		in := &ec2.CreateVolumeInput{
			AvailabilityZone:  aws.String(p.AvailabilityZone),
			Size:              aws.Int32(p.SizeGiB),
			DryRun:            dry,
			TagSpecifications: strattTagSpec(ec2types.ResourceTypeVolume, corr),
		}
		if p.VolumeType != "" {
			in.VolumeType = ec2types.VolumeType(p.VolumeType)
		}
		var out *ec2.CreateVolumeOutput
		if out, err = api.CreateVolume(ctx, in); err == nil {
			outputs = map[string]any{"volumeId": aws.ToString(out.VolumeId)}
		}
	case actionCreateVPC:
		var p struct {
			CidrBlock string `json:"cidrBlock"`
		}
		if e := decodeArgs(req, &p); e != nil {
			return e
		}
		if p.CidrBlock == "" {
			return status.Errorf(codes.InvalidArgument, "%s requires cidrBlock", action)
		}
		var out *ec2.CreateVpcOutput
		if out, err = api.CreateVpc(ctx, &ec2.CreateVpcInput{
			CidrBlock:         aws.String(p.CidrBlock),
			DryRun:            dry,
			TagSpecifications: strattTagSpec(ec2types.ResourceTypeVpc, corr),
		}); err == nil {
			outputs = map[string]any{"vpcId": aws.ToString(out.Vpc.VpcId)}
		}
	case actionCreateSubnet:
		var p struct {
			VpcID            string `json:"vpcId"`
			CidrBlock        string `json:"cidrBlock"`
			AvailabilityZone string `json:"availabilityZone"`
		}
		if e := decodeArgs(req, &p); e != nil {
			return e
		}
		if p.VpcID == "" || p.CidrBlock == "" {
			return status.Errorf(codes.InvalidArgument, "%s requires vpcId and cidrBlock", action)
		}
		in := &ec2.CreateSubnetInput{
			VpcId:             aws.String(p.VpcID),
			CidrBlock:         aws.String(p.CidrBlock),
			DryRun:            dry,
			TagSpecifications: strattTagSpec(ec2types.ResourceTypeSubnet, corr),
		}
		if p.AvailabilityZone != "" {
			in.AvailabilityZone = aws.String(p.AvailabilityZone)
		}
		var out *ec2.CreateSubnetOutput
		if out, err = api.CreateSubnet(ctx, in); err == nil {
			outputs = map[string]any{"subnetId": aws.ToString(out.Subnet.SubnetId)}
		}
	}
	if err != nil {
		if req.GetDryRun() && isDryRunSuccess(err) {
			return s.terminalDryRun(stream, req, "dry-run ok: would provision "+op, "actions/awsec2/"+op+".output")
		}
		return s.terminalFailure(stream, req, fmt.Errorf("%s: %w", action, err))
	}
	return s.terminalResource(stream, req, op, outputs)
}

// decodeArgs unmarshals the InvokeRequest args into dst, or returns InvalidArgument.
func decodeArgs(req *pluginv1.InvokeRequest, dst any) error {
	if args := req.GetArgs(); args != nil && len(args.GetBytes()) > 0 {
		if err := json.Unmarshal(args.GetBytes(), dst); err != nil {
			return status.Errorf(codes.InvalidArgument, "%s: invalid args: %v", req.GetAction(), err)
		}
	}
	return nil
}

// progress streams one non-terminal INFO TaskEvent (typed descent, §1.8).
func (s *Server) progress(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, msg string) error {
	return stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level:         pluginv1.TaskEvent_LEVEL_INFO,
		Message:       msg,
		At:            timestamppb.Now(),
		CorrelationId: req.GetEnvelope().GetCorrelationId(),
	}})
}

// terminalResource emits the terminal ok event for a fire-and-return resource Action:
// typed per-op outputs + the output contract, NO Entity (C3 models these as Entities).
func (s *Server) terminalResource(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, op string, outputs map[string]any) error {
	raw, err := json.Marshal(outputs)
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("awsec2/%s: marshal outputs: %w", op, err))
	}
	s.log.Info("provisioned resource", "op", op, "outputs", outputs)
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level:         pluginv1.TaskEvent_LEVEL_INFO,
			Message:       "provisioned " + op,
			At:            timestamppb.Now(),
			CorrelationId: req.GetEnvelope().GetCorrelationId(),
			Terminal:      true,
			Ok:            true,
		},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: raw},
			OutputContract: &pluginv1.ContractRef{SchemaId: "actions/awsec2/" + op + ".output"},
		},
	})
}
