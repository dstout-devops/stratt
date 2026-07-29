package ansible

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveContentInstallsAndObservesBack runs the SHIPPED apache and tomcat plays against REAL
// bare nodes, through the real EE image, and asserts what the estate claims they do.
//
// It exists because every leg this session that was declared-but-unexecuted turned out to have a
// defect in it — a tofu module whose reserved output would have failed on its first real apply, a
// Destroy verb that exited zero having touched nothing, an init that worked once per pod. These
// three content roots are the newest such leg, and an estate that LOADS proves only that the YAML
// parses.
//
// THE FALSIFICATION IS THE POINT. Each node is asserted to start WITHOUT its package, so a green
// run means the remediation installed it rather than the image having shipped it. Without that
// baseline the whole test would pass just as well against a pre-baked image — which is exactly the
// weakness of converging nginx on a node that already runs nginx, and the reason apache and tomcat
// got bare nodes.
//
// It also covers the claim that carries the most weight and had been run against exactly one
// distro: that `httpd`-vs-`apache2`-vs-`tomcat10` belongs to CONTENT and never to the Intent.
// apache goes onto alpine/apk, tomcat onto debian/apt, from Intents that name no package manager.
//
// Run: `task dev:content:proof`.
func TestLiveContentInstallsAndObservesBack(t *testing.T) {
	if os.Getenv("STRATT_LIVE_CONTENT") == "" {
		t.Skip("set STRATT_LIVE_CONTENT=1 to run the live content proof (needs docker + stratt-ee:dev)")
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	keyDir := os.Getenv("STRATT_LIVE_KEYDIR")
	if keyDir == "" {
		t.Skip("set STRATT_LIVE_KEYDIR to a directory holding id_ed25519 + id_ed25519.pub")
	}

	net := "stratt-content-proof"
	_ = exec.Command("docker", "network", "create", net).Run()
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", net).Run() })

	for _, tc := range []struct {
		app       string        // the estate's package name — what the Intent says
		image     string        // the bare node, deliberately WITHOUT the package
		bootstrap string        // sshd + python only; never the application
		pkg       string        // what the distro actually calls it
		playbook  string        // the SHIPPED play, run verbatim
		extra     []string      // the Workflow's declared extraVars
		port      string        // the resolved desired port
		wait      time.Duration // apt is slower than apk
	}{
		{
			app:   "apache",
			image: "alpine:3.22",
			bootstrap: "apk add --no-cache openssh python3 >/dev/null 2>&1 && ssh-keygen -A && " +
				"mkdir -p /root/.ssh && cp /authorized_keys /root/.ssh/authorized_keys && " +
				"chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys && " +
				"sed -i 's/^#*PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config && " +
				"exec /usr/sbin/sshd -D -e",
			pkg:      "apache2",
			playbook: "content/apache/apache-configure.yml",
			extra:    []string{"apache_port=8080"},
			port:     "8080",
			wait:     60 * time.Second,
		},
		{
			app:   "tomcat",
			image: "debian:12-slim",
			bootstrap: "export DEBIAN_FRONTEND=noninteractive && apt-get update -qq >/dev/null 2>&1 && " +
				"apt-get install -y -qq openssh-server python3 >/dev/null 2>&1 && " +
				"mkdir -p /run/sshd /root/.ssh && cp /authorized_keys /root/.ssh/authorized_keys && " +
				"chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys && " +
				"sed -i 's/^#*PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config && " +
				"exec /usr/sbin/sshd -D -e",
			pkg:      "tomcat10",
			playbook: "content/tomcat/tomcat-configure.yml",
			extra:    []string{"tomcat_port=8080", "tomcat_home=/usr/share/tomcat10", "tomcat_conf_dir=/etc/tomcat10"},
			port:     "8080",
			wait:     180 * time.Second,
		},
	} {
		t.Run(tc.app, func(t *testing.T) {
			node := fmt.Sprintf("stratt-proof-%s", tc.app)
			_ = exec.Command("docker", "rm", "-f", node).Run()
			run(t, "docker", "run", "-d", "--name", node, "--network", net,
				"-v", filepath.Join(keyDir, "id_ed25519.pub")+":/authorized_keys:ro",
				tc.image, "/bin/sh", "-c", tc.bootstrap)
			t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", node).Run() })
			awaitSSHD(t, node, tc.wait)

			// ── BASELINE: the package is ABSENT ─────────────────────────────────────────
			// Asserted, not assumed. If the image ever starts shipping it, every assertion
			// below becomes vacuous and this test would keep passing while proving nothing.
			if installed(t, node, tc.pkg) {
				t.Fatalf("%s is ALREADY installed on the bare %s node. The install assertion below "+
					"would then prove nothing — a converge that changed nothing would look identical "+
					"to one that worked", tc.pkg, tc.image)
			}
			t.Logf("baseline: %s is absent on %s", tc.pkg, tc.image)

			// ── THE SHIPPED PLAY, RUN VERBATIM ──────────────────────────────────────────
			out := runPlay(t, repo, keyDir, net, node, tc.playbook, tc.extra)

			// ── The package is now there, because the play put it there ─────────────────
			if !installed(t, node, tc.pkg) {
				t.Fatalf("%s is still not installed after the converge.\n--- play output ---\n%s", tc.pkg, out)
			}
			t.Logf("INSTALLED: %s is present on %s — the play put it there", tc.pkg, tc.image)

			// ── The observe-back half: the Finding can only resolve on what is REPORTED ──
			// A play that converged correctly and reported nothing would leave the drift
			// Finding open forever, so the fact-back is as load-bearing as the install.
			for _, want := range []string{"stratt_facets", "app.config", "software.package"} {
				if !strings.Contains(out, want) {
					t.Errorf("the play never reported %q. Without the fact-back the drift Finding "+
						"cannot resolve and the same remediation is offered forever (§1.8)\n"+
						"--- play output ---\n%s", want, out)
				}
			}
			if !strings.Contains(out, tc.port) {
				t.Errorf("the reported app.config carries no port %q", tc.port)
			}
		})
	}
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// awaitSSHD polls until the node answers on 22 — the bootstrap installs packages first, and how
// long that takes differs by an order of magnitude between apk and apt.
func awaitSSHD(t *testing.T, node string, limit time.Duration) {
	t.Helper()
	// Readiness is read from sshd's OWN startup line, not from a port probe: debian:12-slim ships
	// neither netstat nor ss, so the probe reported "never opened sshd" for a node whose log said
	// "Server listening on 0.0.0.0 port 22". A readiness check that depends on tooling the target
	// may not have is a check that fails for reasons unrelated to readiness.
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("docker", "logs", node).CombinedOutput()
		if strings.Contains(string(out), "Server listening on 0.0.0.0 port 22") {
			return
		}
		time.Sleep(2 * time.Second)
	}
	logs, _ := exec.Command("docker", "logs", "--tail", "20", node).CombinedOutput()
	t.Fatalf("%s never opened sshd within %s\n%s", node, limit, logs)
}

func installed(t *testing.T, node, pkg string) bool {
	t.Helper()
	err := exec.Command("docker", "exec", node, "sh", "-c",
		fmt.Sprintf("apk info -e %s 2>/dev/null | grep -q . || dpkg -s %s >/dev/null 2>&1", pkg, pkg)).Run()
	return err == nil
}

// runPlay executes the shipped playbook out of the repo's content root, through the real EE image,
// against the node container — the same play, the same variables the Workflow declares, no fixture.
func runPlay(t *testing.T, repo, keyDir, net, node, playbook string, extra []string) string {
	t.Helper()
	args := []string{
		"run", "--rm", "--network", net,
		"-v", filepath.Join(repo, "plugins", "ansible", "estate") + ":/project:ro",
		"-v", filepath.Join(keyDir, "id_ed25519") + ":/key:ro",
		"-e", "ANSIBLE_HOST_KEY_CHECKING=False",
		"-e", "ANSIBLE_STDOUT_CALLBACK=default",
		"--entrypoint", "ansible-playbook",
		"stratt-ee:dev",
		"-i", node + ",",
		"-u", "root", "--private-key", "/key",
		"/project/" + playbook,
	}
	for _, e := range extra {
		args = append(args, "-e", e)
	}
	// -v so set_fact's stratt_facets payload appears in the output: the fact-back is the half
	// that makes a Finding resolvable, and a play that converged silently would look identical
	// to one that reported.
	args = append(args, "-v")
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ansible-playbook %s failed: %v\n%s", playbook, err, out)
	}
	return string(out)
}
