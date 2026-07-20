// The AWS EC2 Connector plugin (ADR-0046 Phase B) — extracted out of the control
// plane into its own build/test/CI unit. It ships BOTH capabilities of the awsec2
// Connector: a Syncer (Observe EC2 instances) and an Action (Invoke the targetless
// "awsec2/create-vm"). It imports the lean plugin SDK and aws-sdk-go-v2, and
// NOTHING from core/: the AWS SDK no longer touches the control plane's dependency
// graph (the module-isolation discipline of ADR-0046).
//
// This is the FIRST plugin to implement the Action/Invoke verb over the sovereign
// plugin port. The plugin holds no graph write path (§1.2): it proposes typed
// ObservedEntity / InvokeResult values on the wire; the core-side host governs
// what it may write (ownership, identity gating, Run provenance).
module github.com/dstout-devops/stratt/plugins/awsec2

go 1.25.0

require (
	github.com/aws/aws-sdk-go-v2 v1.42.1
	github.com/aws/aws-sdk-go-v2/config v1.32.29
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.316.0
	github.com/aws/smithy-go v1.27.3
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
)

require (
	github.com/aws/aws-sdk-go-v2/credentials v1.19.28 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.30 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.30 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.30 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.31 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.30 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.4.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.32.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.37.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.44.0 // indirect
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.44.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
