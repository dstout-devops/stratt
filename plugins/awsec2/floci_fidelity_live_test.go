package awsec2

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// TestFociFidelityBoundary pins WHAT THE DEV EC2 BACKEND ACTUALLY DOES, because for
// months several documents asserted something it does not.
//
// The claim "floci runs real Docker containers as EC2 instances — SSH-able,
// cloud-init/UserData/IMDS" appeared in ADR-0093, ADR-0112, ADR-0116, demos/README.md,
// the compose file and two demo READMEs. Measured (2026-07-27, floci 1.5.33) the
// network half is entirely real and the SSH half is not: no AMI carries an sshd binary,
// user-data is never executed, and RegisterImage accepts a custom image then ignores it
// at launch. ADR-0112's own module README had already booked this as an open
// "DeepWiki-vs-docs conflict on floci's instance realness … settled here"; it is settled,
// and the docs were the wrong half.
//
// That mattered concretely: it decides whether a provisioned instance can be CONVERGED,
// which is the join between the provisioning leg and the configuration leg of the estate
// story. A doc read instead of a wire cost that answer.
//
// So this test asserts the boundary in BOTH directions:
//
//   - the network write surface WORKS — it is what the opentofu aws-network module
//     (deploy/tofu-modules/aws-network) runs against, so a floci regression here breaks
//     the provisioning leg and must fail loudly rather than at demo time;
//   - the instance is NOT SSH-able — an inverted assertion on purpose. If floci later
//     grows sshd this test FAILS, and that failure is the signal to re-read the docs
//     this test exists to keep honest and to revisit the converge substrate. A fidelity
//     claim nothing checks is how the last one rotted (§1.8 — hide mechanism, never
//     fidelity).
//
// Gated on STRATT_LIVE_EC2_ENDPOINT, so it is a no-op in normal `task ci`:
//
//	STRATT_LIVE_EC2_ENDPOINT=http://localhost:4566 AWS_ACCESS_KEY_ID=testing \
//	  AWS_SECRET_ACCESS_KEY=testing AWS_REGION=us-east-1 \
//	  go test ./ -run FociFidelity -v
func TestFociFidelityBoundary(t *testing.T) {
	endpoint := os.Getenv("STRATT_LIVE_EC2_ENDPOINT")
	if endpoint == "" {
		t.Skip("set STRATT_LIVE_EC2_ENDPOINT to run the floci fidelity-boundary probe")
	}
	srv := NewServer(Config{Region: "us-east-1", Endpoint: endpoint}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	run := func(action string, args any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(args)
		stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
		if err := srv.Invoke(&pluginv1.InvokeRequest{Action: action, Args: &pluginv1.Payload{Bytes: raw}}, stream); err != nil {
			t.Fatalf("%s transport: %v", action, err)
		}
		term := stream.sent[len(stream.sent)-1]
		if !term.GetEvent().GetTerminal() || !term.GetEvent().GetOk() {
			t.Fatalf("%s not ok: %q", action, term.GetEvent().GetMessage())
		}
		var m map[string]any
		_ = json.Unmarshal(term.GetResult().GetOutputs().GetBytes(), &m)
		return m
	}

	// ── the network write surface the aws-network tofu module depends on ──────────
	vpc, _ := run("awsec2/create-vpc", map[string]any{"cidrBlock": "10.71.0.0/16"})["vpcId"].(string)
	subnet, _ := run("awsec2/create-subnet", map[string]any{
		"vpcId": vpc, "cidrBlock": "10.71.1.0/24", "availabilityZone": "us-east-1a",
	})["subnetId"].(string)
	sg, _ := run("awsec2/create-security-group", map[string]any{
		"groupName": "fidelity-probe", "description": "fidelity probe", "vpcId": vpc,
	})["securityGroupId"].(string)
	if vpc == "" || subnet == "" || sg == "" {
		t.Fatalf("floci network write regressed: vpc=%q subnet=%q sg=%q — the aws-network module cannot build against this backend", vpc, subnet, sg)
	}
	t.Logf("network write OK: vpc=%s subnet=%s sg=%s", vpc, subnet, sg)

	// ── an instance, built through the Action the estate actually launches ────────
	//
	// NOTE the params this CANNOT pass: `subnetId`, `availabilityZone`, `keyName`.
	// awsec2/create-vm's input Contract is `additionalProperties: false` over
	// {region, endpoint, instanceType, ami, name, projectKind, projectLabels}, and
	// createVMParams has no field for any of them — so the Action cannot place an
	// instance in a subnet at all. That is a defect on the provisioning path, not a
	// limitation of this probe; it is recorded in the enterprise-readiness tracker.
	inst := run("awsec2/create-vm", map[string]any{
		"region": "us-east-1", "ami": "ami-ubuntu2204", "instanceType": "t3.micro", "name": "fidelity-probe",
	})
	id, _ := inst["instanceId"].(string)
	if id == "" {
		t.Fatalf("create-vm returned no instanceId: %+v", inst)
	}
	t.Logf("instance %s created (unplaced — see the note above)", id)

	// ── the SSH boundary: published port, dead backend ────────────────────────────
	//
	// Docker's proxy ACCEPTS the TCP connection on a published port whether or not
	// anything listens inside the container, so "connect succeeded" proves nothing.
	// A real sshd sends its `SSH-2.0-` identification banner unprompted and
	// immediately (RFC 4253 §4.2); a dead backend sends nothing and EOFs. The banner
	// read is therefore the only honest probe.
	port := publishedSSHPort(t, id)
	if port == "" {
		t.Skip("cannot discover the instance's published SSH port (no docker CLI) — network-fidelity half asserted, SSH half skipped")
	}
	banner := readSSHBanner(t, "127.0.0.1:"+port)
	if strings.HasPrefix(banner, "SSH-") {
		t.Fatalf("floci instance %s NOW SPEAKS SSH (banner %q).\n"+
			"This test is inverted on purpose and this failure is good news: the dev EC2 backend "+
			"gained a guest sshd.\nUpdate the fidelity claims this test guards (demos/README.md, "+
			"docs/enterprise-readiness.md PLG-1, ADR-0093/0112/0116) and revisit whether the "+
			"capstone's converge leg can move onto floci-provisioned hosts.", id, banner)
	}
	t.Logf("SSH boundary holds: published port %s accepts TCP but serves no banner (%q) — "+
		"floci instances are real containers, NOT converge-able hosts", port, banner)
}

var flociContainerRE = regexp.MustCompile(`0\.0\.0\.0:(\d+)->22/tcp`)

// publishedSSHPort finds the host port floci published for this instance's :22, via the
// docker CLI. Returns "" when docker is unavailable — this is a dev-harness probe, so
// shelling out is legitimate here and nowhere else in the plugin.
//
// Polls, because RunInstances returns as soon as the instance is recorded and the backing
// container appears a moment later. Checking once made this skip rather than assert, which
// is the quiet way a probe stops probing.
func publishedSSHPort(t *testing.T, instanceID string) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Log("docker CLI absent — cannot locate the instance container")
		return ""
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		out, err := exec.CommandContext(ctx, "docker", "ps",
			"--filter", "name=floci-ec2-"+instanceID, "--format", "{{.Ports}}").Output()
		cancel()
		if err == nil {
			if m := flociContainerRE.FindStringSubmatch(string(out)); len(m) == 2 {
				return m[1]
			}
		}
		if time.Now().After(deadline) {
			t.Logf("no container named floci-ec2-%s published :22 within 30s", instanceID)
			return ""
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// readSSHBanner dials and reads whatever the peer volunteers within a short window,
// without writing anything. Returns "" on EOF/refusal/timeout — all of which mean
// "no sshd here".
func readSSHBanner(t *testing.T, addr string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	return strings.TrimSpace(string(buf[:n]))
}
