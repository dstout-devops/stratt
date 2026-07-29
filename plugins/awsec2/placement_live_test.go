package awsec2

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// TestLivePlacementIntoARealSubnet is PRV-2's proof: an instance built INTO a subnet that was
// itself built from an Intent, against the real EC2 API.
//
// The defect it closes was reasoned about for a long time and never executed. `placement.subnet`
// holds an Intent/Subnet NAME — the only thing Git can hold — and `compute-build` bound it straight
// into `subnetId`, so RunInstances received "app-subnet" where AWS requires "subnet-0abc…". The
// Workflow's own comment claimed to be "where the two meet"; no translation existed. Measured:
// `--subnet-id app-subnet` → InvalidSubnetID.NotFound.
//
// This test asserts BOTH directions, because only the pair is convincing:
//
//   - the resolved provider-native ref places the instance in the real subnet, confirmed by asking
//     the API which subnet the instance actually landed in — not by trusting the build's report;
//   - the raw Intent NAME, the exact value the estate used to send, is REJECTED by the substrate.
//     Without that half the first assertion would pass just as well against a backend that ignored
//     placement entirely, which is precisely the class of false green this repo keeps finding.
func TestLivePlacementIntoARealSubnet(t *testing.T) {
	endpoint := os.Getenv("STRATT_LIVE_EC2_ENDPOINT")
	if endpoint == "" {
		t.Skip("set STRATT_LIVE_EC2_ENDPOINT to run the live placement proof")
	}
	const region = "us-east-1"
	for k, v := range map[string]string{
		"AWS_ACCESS_KEY_ID": "testing", "AWS_SECRET_ACCESS_KEY": "testing", "AWS_REGION": region,
	} {
		t.Setenv(k, v)
	}

	// Stand up the network the way the estate does: a VPC + a subnet. The subnet id is what the
	// reconcile would have resolved from `placement.subnet: app-subnet` via the built subnet's
	// aws.subnetId identity key.
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1e9)
	vpc := awsCLI(t, endpoint, region, "ec2", "create-vpc", "--cidr-block", "10.60.0.0/16",
		"--query", "Vpc.VpcId", "--output", "text")
	subnetID := awsCLI(t, endpoint, region, "ec2", "create-subnet", "--vpc-id", vpc,
		"--cidr-block", "10.60."+suffix[len(suffix)-1:]+".0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	t.Logf("the network the build places into: vpc=%s subnet=%s", vpc, subnetID)

	s := NewServer(Config{Region: region, Endpoint: endpoint},
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	build := func(t *testing.T, subnet string) (*captureStream[pluginv1.InvokeResponse], error) {
		t.Helper()
		args, _ := json.Marshal(createVMParams{
			Region: region, AMI: "ami-0linuxbaseline000", InstanceType: "t3.micro",
			Name: "placed-" + suffix, SubnetID: subnet,
		})
		cap := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
		err := s.Invoke(&pluginv1.InvokeRequest{
			Action: "awsec2/create-vm", Args: &pluginv1.Payload{Bytes: args},
		}, cap)
		return cap, err
	}

	// ── The fix: the RESOLVED provider-native ref places the instance ────────────────────
	cap, err := build(t, subnetID)
	if err != nil {
		t.Fatalf("build with the resolved subnet ref failed: %v", err)
	}
	if len(cap.sent) == 0 {
		t.Fatal("the build streamed nothing")
	}
	term := cap.sent[len(cap.sent)-1]
	if !term.GetEvent().GetOk() {
		t.Fatalf("build with the resolved subnet ref did not succeed: %s", term.GetEvent().GetMessage())
	}
	var out map[string]any
	if err := json.Unmarshal(term.GetResult().GetOutputs().GetBytes(), &out); err != nil {
		t.Fatalf("outputs: %v", err)
	}
	instanceID, _ := out["instanceId"].(string)
	if instanceID == "" {
		t.Fatal("the build reported no instanceId")
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "run", "--rm", "--network", "host",
			"-e", "AWS_ACCESS_KEY_ID=testing", "-e", "AWS_SECRET_ACCESS_KEY=testing",
			"amazon/aws-cli:2.32.5", "--endpoint-url", endpoint, "--region", region,
			"ec2", "terminate-instances", "--instance-ids", instanceID).Run()
	})

	// Ask the API where it ACTUALLY landed. The build's own report is not evidence of placement.
	got := awsCLI(t, endpoint, region, "ec2", "describe-instances", "--instance-ids", instanceID,
		"--query", "Reservations[0].Instances[0].SubnetId", "--output", "text")
	if got != subnetID {
		t.Fatalf("instance %s landed in subnet %q, want %q — the placement did not take effect",
			instanceID, got, subnetID)
	}
	t.Logf("PLACED: %s is in %s — the resolved ref reached RunInstances and the substrate honoured it",
		instanceID, got)

	// ── The falsification: the raw Intent NAME is rejected ───────────────────────────────
	// This is the exact value the estate sent before ADR-0147. If this ever starts succeeding,
	// the backend has stopped validating placement and the assertion above proves nothing.
	// The outcome is read from the TERMINAL EVENT, not from Invoke's returned error. A domain
	// failure rides the typed descent channel (§1.8) and Invoke returns nil — so `err == nil`
	// reads as success for a build that failed. Checking the wrong signal made this half of the
	// proof report a floci fidelity regression that was not there.
	bad, err := build(t, "app-subnet")
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	badTerm := bad.sent[len(bad.sent)-1].GetEvent()
	if badTerm.GetOk() {
		t.Error("building with the raw Intent NAME 'app-subnet' SUCCEEDED. Either the substrate " +
			"stopped validating subnet ids — which would make the placement assertion above " +
			"vacuous — or the name is being resolved somewhere it should not be")
	} else if !strings.Contains(badTerm.GetMessage(), "InvalidSubnetID") {
		t.Logf("the raw Intent name failed, though not with InvalidSubnetID: %s", badTerm.GetMessage())
	} else {
		t.Logf("falsified: the raw Intent name is rejected by the substrate (%s) — which is exactly "+
			"why the reconcile must resolve it to a provider-native id", badTerm.GetMessage())
	}
}

// awsCLI runs one aws-cli command against the dev endpoint and returns its trimmed output.
func awsCLI(t *testing.T, endpoint, region string, args ...string) string {
	t.Helper()
	full := append([]string{"run", "--rm", "--network", "host",
		"-e", "AWS_ACCESS_KEY_ID=testing", "-e", "AWS_SECRET_ACCESS_KEY=testing",
		"amazon/aws-cli:2.32.5", "--endpoint-url", endpoint, "--region", region}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("aws %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
