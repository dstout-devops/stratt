package dns

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// The LIVE proof of ADDR-1's registered producer, against REAL BIND 9 — the reference
// implementation of the protocol, not a mock of it (`task dev:dns:up`).
//
// The chain, end to end, and every link falsifiable:
//
//	a machine on a network  →  the fleet Apply writes an RFC 2136 update
//	→  an INDEPENDENT reader (dig, inside the server's own container) sees the record
//	→  Observe projects it as mgmt.address
//	→  a peer whose ONLY resolver is that server reaches the machine BY THAT NAME
//	→  deregister, and the same reach FAILS
//
// The last line is what makes the rest mean anything. Without it the proof cannot tell
// "Stratt registered a name and it works" from "the name happened to resolve some other
// way" — which is not a hypothetical: docker's embedded DNS already resolves container
// names on this network, and an earlier version of this workstream had exactly that kind
// of accidental pass. So the peer is given ONE resolver, the zone starts with no host
// records at all (deploy/dev/dns/zone.tmpl), and removal has to break it.
//
// Skipped without STRATT_DNS_SERVER, like every other live test here.
func TestLiveDnsRegisterObserveReach(t *testing.T) {
	server := os.Getenv("STRATT_DNS_SERVER")
	if server == "" {
		t.Skip("set STRATT_DNS_SERVER (task dev:dns:up) to run the live DNS proof")
	}
	zone := envOr("STRATT_DNS_ZONE", "estate.stratt.test")
	key := TSIGKey{
		Name:      envOr("STRATT_DNS_TSIG_NAME", "stratt-dev"),
		Secret:    tsigSecret(t),
		Algorithm: os.Getenv("STRATT_DNS_TSIG_ALGORITHM"),
	}
	network := envOr("STRATT_DNS_GUEST_NETWORK", "vspheresim")
	image := envOr("STRATT_DNS_GUEST_IMAGE", "stratt/vspheresim-guest:dev")
	sshKey := os.Getenv("STRATT_DNS_GUEST_KEY")
	if sshKey == "" {
		t.Fatal("STRATT_DNS_GUEST_KEY is unset — the reach half would be silently skipped, which is the " +
			"exact failure this test exists to close (a coordinate nobody connected to)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	srv := NewServer(Config{Server: server, Zones: []string{zone}, TSIG: key}, discard())

	// A machine on the network. A UNIQUE name per run, deliberately: a leftover guest or
	// a leftover record from a previous run would satisfy every assertion below
	// instantly and prove nothing about THIS run — the vspheresim proof shipped that bug
	// once already.
	host := fmt.Sprintf("web-%x", time.Now().UnixNano()&0xffffff)
	guest := startGuest(t, ctx, network, image, host)
	addr := containerIP(t, ctx, guest, network)
	t.Logf("guest %s is at %s on network %s", host, addr, network)

	fqdn := host + "." + zone

	// ── 1. The fleet Apply: the record's data is the TARGET's coordinate, not a literal ──
	cap := &applyCapture{ctx: ctx}
	if err := srv.Apply(&pluginv1.ApplyRequest{
		Desired: &pluginv1.Payload{Bytes: []byte(`{"zone":"` + zone + `"}`)},
		Targets: []*pluginv1.ApplyTarget{{Name: host, Address: addr}},
	}, cap); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	final := cap.msgs[len(cap.msgs)-1].GetEvent()
	if !final.GetOk() {
		t.Fatalf("Apply did not converge: %s", final.GetMessage())
	}

	// ── 2. An INDEPENDENT reader. `dig` inside the server's own container, not our own
	// AXFR: a proof that only ever reads through the client that wrote cannot distinguish
	// "the record is in the zone" from "our reader agrees with our writer".
	if got := digA(t, ctx, fqdn); got != addr {
		t.Fatalf("dig %s = %q, want %q — the record is not in the server's zone", fqdn, got, addr)
	}
	t.Logf("dig (independent reader, inside the server): %s A %s", fqdn, addr)

	// ── 3. Observe: the coordinate is projected because it was READ BACK, not asserted ──
	obs := &observeCapture{ctx: ctx}
	if err := srv.Observe(&pluginv1.ObserveRequest{}, obs); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	var found *pluginv1.ObservedEntity
	for _, e := range obs.msgs[0].GetEntities() {
		if e.GetIdentityKeys()["dns.fqdn"] == fqdn {
			found = e
		}
	}
	if found == nil {
		t.Fatalf("Observe projected no Entity for %s", fqdn)
	}
	if got := addressOf(t, found); got != fqdn {
		t.Fatalf("mgmt.address = %q, want the NAME %q — the coordinate is the record, never its data", got, fqdn)
	}
	t.Logf("observed: %s carries mgmt.address %s", fqdn, addressOf(t, found))

	// ── 4. USE it. One resolver, and it is the server Stratt wrote to ────────────────
	dnsIP := containerIP(t, ctx, "stratt-dev-dns", network)
	out, err := sshVia(ctx, network, image, sshKey, dnsIP, fqdn, "hostname -f")
	if err != nil {
		t.Fatalf("reach %s: %v\n%s", fqdn, err, out)
	}
	if !strings.Contains(out, host) {
		t.Fatalf("the host at %s reported %q, which does not contain %q — something else answered", fqdn, strings.TrimSpace(out), host)
	}
	t.Logf("REACHED %s — it reports %q, and the only resolver that knows that name is the one Stratt registered it with",
		fqdn, strings.TrimSpace(out))

	// ── 5. Falsify. Remove the record; the SAME reach must now fail ──────────────────
	inv := &invokeCapture{ctx: ctx}
	if err := srv.Invoke(&pluginv1.InvokeRequest{
		Action: actionDeregister,
		Args:   &pluginv1.Payload{Bytes: []byte(`{"zone":"` + zone + `","name":"` + host + `","type":"A"}`)},
	}, inv); err != nil {
		t.Fatalf("deregister: %v", err)
	}
	if !inv.msgs[len(inv.msgs)-1].GetEvent().GetOk() {
		t.Fatalf("deregister failed: %s", inv.msgs[len(inv.msgs)-1].GetEvent().GetMessage())
	}
	if out, err := sshVia(ctx, network, image, sshKey, dnsIP, fqdn, "hostname -f"); err == nil {
		t.Fatalf("%s STILL resolves after the record was removed — the reach above was not caused by "+
			"Stratt's record, and every assertion before this line was passing for another reason\n%s", fqdn, out)
	}
	t.Logf("falsified: with the record removed, %s reaches nothing — the name worked because Stratt wrote it", fqdn)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// tsigSecret reads the key from the generated env file (task dev:dns:key) rather than
// from a command line or a literal: material never appears in a process table, a shell
// history, or this repo (§2.5).
func tsigSecret(t *testing.T) string {
	t.Helper()
	if s := os.Getenv("STRATT_DNS_TSIG_SECRET"); s != "" {
		return s
	}
	path := os.Getenv("STRATT_DNS_TSIG_ENV")
	if path == "" {
		t.Fatal("no TSIG key: set STRATT_DNS_TSIG_SECRET or STRATT_DNS_TSIG_ENV (task dev:dns:key writes the latter)")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k == "STRATT_DNS_TSIG_SECRET" {
			return v
		}
	}
	t.Fatalf("%s carries no STRATT_DNS_TSIG_SECRET", path)
	return ""
}

// startGuest runs one guest container on the network and registers its teardown.
func startGuest(t *testing.T, ctx context.Context, network, image, name string) string {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--rm",
		"--name", name, "--hostname", name, "--network", network, image).CombinedOutput()
	if err != nil {
		t.Fatalf("start guest %s: %v\n%s", name, err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	// sshd needs a moment to generate host keys on first boot.
	time.Sleep(2 * time.Second)
	return name
}

func containerIP(t *testing.T, ctx context.Context, container, network string) string {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		`{{ (index .NetworkSettings.Networks "`+network+`").IPAddress }}`, container).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect %s on %s: %v\n%s", container, network, err, out)
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		t.Fatalf("%s has no address on network %s", container, network)
	}
	return ip
}

// digA asks the authoritative server directly, from INSIDE its own container, using the
// bind-tools that ship with it. Deliberately not our own AXFR client and not the host's
// resolver: this is the reader that has no stake in the answer.
func digA(t *testing.T, ctx context.Context, fqdn string) string {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", "exec", "stratt-dev-dns",
		"dig", "+short", "@127.0.0.1", fqdn, "A").CombinedOutput()
	if err != nil {
		t.Fatalf("dig %s: %v\n%s", fqdn, err, out)
	}
	return strings.TrimSpace(string(out))
}

// sshVia reaches a host BY NAME from a peer container whose ONLY resolver is the given
// server. One resolver, on purpose: docker's embedded DNS resolves container names on
// this network, so a peer with the default resolver would reach the host whether or not
// Stratt ever wrote a record — and the test would pass for the wrong reason.
//
// LogLevel=ERROR because ssh's "Permanently added <host>" notice echoes the name into
// captured output, which once made a "the guest reports its own name" assertion match
// ssh's own chatter rather than the guest's answer.
func sshVia(ctx context.Context, network, image, keyPath, resolver, target, remote string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm",
		"--network", network, "--dns", resolver,
		"-v", keyPath+":/key:ro", "--entrypoint", "sh", image,
		"-c", `cp /key /k && chmod 600 /k && exec ssh -i /k `+
			`-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR `+
			`-o BatchMode=yes -o ConnectTimeout=5 "root@$1" "$2"`,
		"stratt-dns-reach", target, remote).CombinedOutput()
	return string(out), err
}
