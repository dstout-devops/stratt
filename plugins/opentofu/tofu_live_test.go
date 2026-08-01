package opentofu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// TestLiveOpenTofuSubnetBuild is the proof the aws-network module never had: a REAL `tofu`
// binary applying the SHIPPED module against the REAL floci EC2 API, with REAL S3 state, driven
// through the opentofu/apply Action exactly as a launched Intent/Subnet build drives it.
//
// It exists because everything about this leg was declared and nothing was executed. The module
// shipped with no lockfile, no `tofu validate` and no apply — there was no tofu binary in the dev
// container to run one — and the build Workflow it was advertised against had never been written.
// A unit test with a scripted fakeTofu cannot catch what that hid: `stratt.name` sat in the
// module's reserved output for months, and the plugin refuses any stratt.*-prefixed label, so the
// first genuine apply would have failed at the projection.
//
// WHAT IT ASSERTS, in order:
//  1. the module APPLIES against the real API (a VPC, subnet, SG, route table, IGW and
//     association actually come into existence);
//  2. the CIDR the subnet gets is the one the core INJECTED as the ipam handle — not one the
//     module chose, which is the whole point of ADR-0111's allocator;
//  3. the built subnet reads back under `aws.subnetId`, the SAME identity scheme the awsec2
//     Syncer uses, so the build's Entity and the Syncer's net.subnet Facet co-own ONE subnet
//     (ADR-0112 D5) rather than silently splitting into two;
//  4. the projection carries stratt.intent/singleton FROM THE LAUNCH — the label the provisioning
//     loop closes on, which cannot travel through the module at all;
//  5. state landed in the injected S3 backend, so a retry converges instead of building a
//     second VPC.
//
// Run: `task dev:tofu:proof` (needs floci + seaweedfs up).
func TestLiveOpenTofuSubnetBuild(t *testing.T) {
	endpoint := os.Getenv("STRATT_LIVE_EC2_ENDPOINT")
	stateEndpoint := os.Getenv("STRATT_LIVE_S3_ENDPOINT")
	if endpoint == "" || stateEndpoint == "" {
		t.Skip("set STRATT_LIVE_EC2_ENDPOINT and STRATT_LIVE_S3_ENDPOINT to run the live tofu build")
	}
	bin, err := filepath.Abs(filepath.Join("..", "..", ".bin", "tofu"))
	if err != nil || !executable(bin) {
		t.Skipf("no pinned tofu at %s (run `task tools:tofu`)", bin)
	}
	moduleRoot, err := filepath.Abs(filepath.Join("..", "..", "deploy", "tofu-modules"))
	if err != nil {
		t.Fatal(err)
	}

	// A unique workspace per run: the workspace IS the state key, so a stale one from a previous
	// run would have tofu converge on that state rather than build.
	workspace := fmt.Sprintf("live-subnet-%d", time.Now().UnixNano()%1e9)
	const wantCIDR = "10.30.11.0/24"
	const singleton = "Intent/Subnet/live-app-subnet"
	// The region is PINNED here and passed to the module, rather than left to the module's own
	// default, because every independent reader below must query the same one. floci is genuinely
	// region-scoped — a subnet created in eu-west-1 is correctly invisible in us-east-1 — so a
	// reader on the wrong region reports "the build created nothing" for a build that worked. That
	// misreading cost an hour here and would have been written up as a floci fidelity regression.
	const region = "eu-west-1"

	// AWS credentials for both floci and seaweedfs. In a Run these arrive as brokered
	// CredentialRefs mounted into the plugin pod (§2.5); here they are the dev harness's own
	// well-known values, set on the process the plugin's subprocess inherits.
	for k, v := range map[string]string{
		"AWS_ACCESS_KEY_ID":     "testing",
		"AWS_SECRET_ACCESS_KEY": "testing",
		"AWS_REGION":            "us-east-1",
	} {
		t.Setenv(k, v)
	}

	s := NewServer(Config{PluginID: "opentofu", ModuleRoot: moduleRoot, TofuBin: bin},
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	args, _ := json.Marshal(map[string]any{
		"module":      "aws-network",
		"workspace":   workspace,
		"projectKind": "subnet",
		"vars": map[string]any{
			"subnet_name":  workspace,
			"aws_endpoint": endpoint,
			"region":       region,
		},
		"projectLabels": map[string]string{"stratt.intent/singleton": singleton},
	})

	// The handles the CORE resolves and injects (ADR-0105/0111, carried to this seam by
	// ADR-0145 D2). The ipam handle is synthesized rather than fetched from NetBox: what is
	// under test here is that an injected allocation REACHES the module and decides the subnet,
	// and a hand-written CIDR proves that more sharply than a NetBox-chosen one would — if the
	// module ignored the injection, the subnet would come back with some other range.
	caps := map[string]*pluginv1.CapabilityHandle{
		"statestore": {
			Kind: "s3",
			Config: map[string]string{
				"bucket":                      "stratt-tofu-state",
				"key":                         workspace + "/terraform.tfstate",
				"endpoints":                   `{"s3":"` + stateEndpoint + `"}`,
				"region":                      "us-east-1",
				"use_path_style":              "true",
				"skip_credentials_validation": "true",
				"skip_requesting_account_id":  "true",
				"skip_metadata_api_check":     "true",
				"skip_region_validation":      "true",
				"skip_s3_checksum":            "true",
			},
		},
		"ipam": {Output: []byte(`{"cidr":"` + wantCIDR + `","vlanId":42}`)},
	}

	t.Cleanup(func() { destroyLive(t, s, workspace, endpoint, region, caps) })

	cap := runInvoke(t, s, string(args), false, caps)
	ev := cap.terminal(t)
	if !ev.GetOk() {
		t.Fatalf("live build failed: %s", ev.GetMessage())
	}
	res := cap.result()
	if res == nil || len(res.GetEntities()) != 1 {
		t.Fatalf("want one projected subnet, got %+v", res)
	}
	ent := res.GetEntities()[0]

	subnetID := ent.GetIdentityKeys()["aws.subnetId"]
	if subnetID == "" {
		t.Fatalf("the build projected no aws.subnetId — identity keys were %v.\n"+
			"Without the scheme the awsec2 Syncer uses, this Entity and the Syncer's net.subnet "+
			"Facet would be two different subnets in the graph (ADR-0112 D5)", ent.GetIdentityKeys())
	}
	if ent.GetKind() != "subnet" {
		t.Errorf("kind = %q, want subnet", ent.GetKind())
	}
	if got := ent.GetLabels()["stratt.intent/singleton"]; got != singleton {
		t.Errorf("correlation label = %q, want %q — the provisioning loop closes on this key and "+
			"it can only arrive from the launch", got, singleton)
	}
	if got := ent.GetLabels()["net.cidr"]; got != wantCIDR {
		t.Errorf("built subnet CIDR = %q, want the INJECTED %q — the allocator decides the range, "+
			"not the module (ADR-0111 D3)", got, wantCIDR)
	}

	// Independent read-back: ask the API itself, not the apply's own report. A module that
	// reported a subnet it did not create would pass every assertion above.
	live := describeSubnet(t, endpoint, region, subnetID)
	if live == "" {
		t.Fatalf("floci does not know subnet %s — the build reported infrastructure that does not exist", subnetID)
	}
	if live != wantCIDR {
		t.Errorf("the REAL subnet %s has CIDR %s, want %s", subnetID, live, wantCIDR)
	}
	t.Logf("built %s (%s) via real tofu against real floci; projected as kind=%s carrying %s",
		subnetID, live, ent.GetKind(), singleton)

	// The state landed in the injected S3 backend. With local state a retry after a lost pod
	// would build a SECOND VPC instead of converging on the first — the reason the statestore
	// handle is resolved for this verb at all (ADR-0145 D2).
	if !stateObjectExists(t, stateEndpoint, "stratt-tofu-state", workspace+"/terraform.tfstate") {
		t.Error("no state object in the injected S3 backend — tofu used local state, so this build is not idempotent across pods")
	}

	// Falsification: run the SAME build again. Idempotent means it converges on the subnet that
	// exists, not that it creates another. Asserting the identity is unchanged is what tells the
	// two apart; a build that quietly made a second VPC would also have "succeeded" twice.
	again := runInvoke(t, s, string(args), false, caps)
	if ev := again.terminal(t); !ev.GetOk() {
		t.Fatalf("second build failed: %s", ev.GetMessage())
	}
	if got := again.result().GetEntities()[0].GetIdentityKeys()["aws.subnetId"]; got != subnetID {
		t.Errorf("re-applying built a DIFFERENT subnet (%s, was %s) — the workspace state is not "+
			"being reused, so every retry leaks infrastructure", got, subnetID)
	}
}

func executable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// describeSubnet asks the EC2 API directly for the subnet's CIDR — an independent reader, so the
// assertion is about the world and not about what the apply said about itself. "" == not found.
func describeSubnet(t *testing.T, endpoint, region, id string) string {
	t.Helper()
	out, err := exec.Command("docker", "run", "--rm", "--network", "host",
		"-e", "AWS_ACCESS_KEY_ID=testing", "-e", "AWS_SECRET_ACCESS_KEY=testing",
		"amazon/aws-cli:2.32.5",
		"--endpoint-url", endpoint, "--region", region,
		"ec2", "describe-subnets", "--subnet-ids", id,
		"--query", "Subnets[0].CidrBlock", "--output", "text").CombinedOutput()
	if err != nil {
		t.Logf("describe-subnets %s: %v: %s", id, err, out)
		return ""
	}
	return strings.TrimSpace(string(out))
}

func stateObjectExists(t *testing.T, endpoint, bucket, key string) bool {
	t.Helper()
	out, err := exec.Command("docker", "run", "--rm", "--network", "host",
		"-e", "AWS_ACCESS_KEY_ID=testing", "-e", "AWS_SECRET_ACCESS_KEY=testing",
		"amazon/aws-cli:2.32.5",
		"--endpoint-url", endpoint, "--region", "us-east-1",
		"s3api", "head-object", "--bucket", bucket, "--key", key).CombinedOutput()
	if err != nil {
		t.Logf("head-object %s/%s: %s", bucket, key, out)
		return false
	}
	return true
}

// destroyCapture collects the Destroy stream.
type destroyCapture struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*pluginv1.DestroyResponse
}

func (c *destroyCapture) Send(m *pluginv1.DestroyResponse) error {
	c.msgs = append(c.msgs, m)
	return nil
}
func (c *destroyCapture) Context() context.Context { return c.ctx }

// destroyLive tears the built network down through the plugin's own DESTROY verb — so the proof
// leaves nothing behind AND the teardown path is exercised rather than assumed. That second half
// is the point: this verb previously ran `tofu destroy` in the process's CWD with no module, no
// env and no state backend, which exits zero, so it reported a successful teardown of nothing.
// Cleaning up through it is what makes the difference observable — if it regresses, the next run
// of this test finds the previous run's VPC still there.
func destroyLive(t *testing.T, s *Server, workspace, endpoint, region string, caps map[string]*pluginv1.CapabilityHandle) {
	t.Helper()
	desired, _ := json.Marshal(map[string]any{
		"module": "aws-network", "workspace": workspace,
		"vars": map[string]any{"subnet_name": workspace, "aws_endpoint": endpoint, "region": region},
	})
	cap := &destroyCapture{ctx: context.Background()}
	err := s.Destroy(&pluginv1.DestroyRequest{
		Desired:              &pluginv1.Payload{Bytes: desired},
		ResolvedCapabilities: caps,
	}, cap)
	if err != nil {
		t.Errorf("teardown: %v", err)
		return
	}
	for _, m := range cap.msgs {
		if ev := m.GetEvent(); ev != nil && ev.GetTerminal() && !ev.GetOk() {
			t.Errorf("teardown did not succeed: %s — the built network is still standing", ev.GetMessage())
		}
	}
}
