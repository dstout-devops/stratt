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
// distro: that `httpd`-vs-`apache2`-vs-`tomcat10`, and every path those packages live at, belongs to
// CONTENT and never to the Intent. apache now runs against all THREE families it claims — Alpine,
// Debian and RedHat — from one Intent that names no package manager and no path. One family was not
// a matrix; it could not distinguish a distro-agnostic play from a distro-specific one, and the two
// families that had never run were both broken (ANS-014).
//
// AND IT PROBES FROM OUTSIDE THE PLAY. `servesHTTP` connects from a third container that shares no
// code, no filesystem and no variables with the converge. Reading only the play's own report would
// still be taking its word for it — which is how a Tomcat that never started once reported a
// converged port, and how apache reported success on two families where it was writing config into
// directories nothing reads.
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

	key := stageKey(t, filepath.Join(keyDir, "id_ed25519"))

	// Bootstraps install sshd and python3 and NOTHING ELSE — never the application under test.
	const (
		sshCommon = "mkdir -p /run/sshd /root/.ssh && cp /authorized_keys /root/.ssh/authorized_keys && " +
			"chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys && " +
			"sed -i 's/^#*PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config && " +
			"exec /usr/sbin/sshd -D -e"
		bootAlpine = "apk add --no-cache openssh python3 >/dev/null 2>&1 && ssh-keygen -A && " + sshCommon
		bootDebian = "export DEBIAN_FRONTEND=noninteractive && apt-get update -qq >/dev/null 2>&1 && " +
			"apt-get install -y -qq openssh-server python3 >/dev/null 2>&1 && " + sshCommon
		bootRocky = "dnf install -y -q openssh-server python3 >/dev/null 2>&1 && ssh-keygen -A && " + sshCommon
	)

	for _, tc := range []struct {
		name      string        // subtest name — app plus the distro FAMILY it exercises
		image     string        // the bare node, deliberately WITHOUT the package
		bootstrap string        // sshd + python only; never the application
		pkg       string        // what the distro actually calls it
		playbook  string        // the SHIPPED play, run verbatim
		extra     []string      // the Workflow's declared extraVars
		port      string        // the resolved desired port
		wait      time.Duration // apt and dnf are slower than apk
	}{
		// THREE FAMILIES FOR APACHE, from one Intent that names no package manager and no path.
		// Two of these three had never run: the play resolved `httpd` vs `apache2` from
		// ansible_os_family and then hard-coded ALPINE's paths for everyone, so on Debian it
		// created a conf.d apache never reads, failed to find httpd.conf and the httpd binary, and
		// reported success anyway because every guard was `failed_when: false` and the observation
		// greped the file the play itself had written (ANS-014). A matrix that runs one family
		// cannot tell a distro-agnostic play from a distro-specific one.
		{
			name:      "apache/Alpine",
			image:     "alpine:3.22",
			bootstrap: bootAlpine,
			pkg:       "apache2",
			playbook:  "content/webapp/apache-configure.yml",
			extra:     []string{"apache_port=8080"},
			port:      "8080",
			wait:      60 * time.Second,
		},
		{
			name:      "apache/Debian",
			image:     "debian:12-slim",
			bootstrap: bootDebian,
			pkg:       "apache2", // same package NAME as Alpine, entirely different layout
			playbook:  "content/webapp/apache-configure.yml",
			extra:     []string{"apache_port=8080"},
			port:      "8080",
			wait:      180 * time.Second,
		},
		{
			name:      "apache/RedHat",
			image:     "rockylinux/rockylinux:9",
			bootstrap: bootRocky,
			pkg:       "httpd", // the branch that shipped for two ADRs without ever executing
			playbook:  "content/webapp/apache-configure.yml",
			extra:     []string{"apache_port=8080"},
			port:      "8080",
			wait:      180 * time.Second,
		},
		{
			name:      "tomcat/Debian",
			image:     "debian:12-slim",
			bootstrap: bootDebian,
			pkg:       "tomcat10",
			playbook:  "content/webapp/tomcat-configure.yml",
			// tomcat_home / tomcat_conf_dir are GONE from the launch interface (ANS-014): the
			// layout is the target's fact, read by content/webapp/vars/tomcat/<family>.yml. If it
			// back as extraVars this would silently pass on Debian and lie about RHEL again.
			extra: []string{"tomcat_port=8080"},
			port:  "8080",
			wait:  180 * time.Second,
		},
		{
			// The last unexecuted layout in the matrix. It closes the gap vars/tomcat/RedHat.yml
			// declared about itself, and it is a genuinely different distribution rather than a
			// second copy of Debian's: RHEL's `tomcat` is Tomcat 9 where Debian's `tomcat10` is
			// Tomcat 10, and it keeps CATALINA_HOME and CATALINA_BASE as ONE directory where Debian
			// splits them. The Intent says `package: tomcat` for both and knows neither.
			name:      "tomcat/RedHat",
			image:     "rockylinux/rockylinux:9",
			bootstrap: bootRocky,
			pkg:       "tomcat",
			playbook:  "content/webapp/tomcat-configure.yml",
			extra:     []string{"tomcat_port=8080"},
			port:      "8080",
			wait:      180 * time.Second,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := "stratt-proof-" + strings.NewReplacer("/", "-").Replace(tc.name)
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
			out := runPlay(t, repo, key, net, node, tc.playbook, tc.extra)

			// ── The package is now there, because the play put it there ─────────────────
			if !installed(t, node, tc.pkg) {
				t.Fatalf("%s is still not installed after the converge.\n--- play output ---\n%s", tc.pkg, out)
			}
			t.Logf("INSTALLED: %s is present on %s — the play put it there", tc.pkg, tc.image)

			// ── THE SERVICE IS ACTUALLY SERVING — checked from OUTSIDE the play ──────────
			// This is ANS-014's lesson made structural. The play now observes the running
			// service rather than greping its own output, but a test that only reads the
			// play's report is still taking the play's word for it. So the probe runs from a
			// third container over the network: it shares no code, no filesystem and no
			// assumptions with the converge, and it fails for a converge that wrote perfect
			// configuration to a service that never came up — which is exactly what the
			// Debian and RedHat paths did while reporting success.
			head := servesHTTP(t, net, node, tc.port)
			t.Logf("SERVING: %s answers on %s — %s", node, tc.port, head)

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
	// Three package databases, because the matrix now covers three families. Asking only apk and
	// dpkg would report "not installed" on RedHat for a package that is there — a baseline check
	// that cannot see the package it is checking for is worse than none, since it would fail the
	// run at the ABSENT assertion and never reach the converge.
	err := exec.Command("docker", "exec", node, "sh", "-c",
		fmt.Sprintf("apk info -e %s 2>/dev/null | grep -q . || dpkg -s %s >/dev/null 2>&1 || rpm -q %s >/dev/null 2>&1",
			pkg, pkg, pkg)).Run()
	return err == nil
}

// servesHTTP connects to the converged node from a THIRD container and returns the HTTP status
// line plus the Server header.
//
// Deliberately out-of-band. The play's own observation and this probe can only agree if the service
// is genuinely up: they share no filesystem, no variables and no code. `wget --spider -S` prints
// the response headers to stderr for any status, so a 403 from a docroot with no index — a normal
// answer from a healthy apache — reads as success here rather than as a failure to serve.
func servesHTTP(t *testing.T, net, node, port string) string {
	t.Helper()
	out, err := exec.Command("docker", "run", "--rm", "--network", net, "busybox:1.37",
		"wget", "--spider", "-S", "-T", "10", fmt.Sprintf("http://%s:%s/", node, port)).CombinedOutput()
	var status, server string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "HTTP/") {
			status = line
		}
		if strings.HasPrefix(line, "Server:") {
			server = line
		}
	}
	if status == "" {
		t.Fatalf("%s:%s served no HTTP response at all (wget err=%v). The converge reported success, "+
			"so either the service never came up or the play observed its own output rather than the "+
			"running system — the ANS-014 failure\n%s", node, port, err, out)
	}
	return status + " · " + server
}

func eeImage() string {
	if img := os.Getenv("STRATT_LIVE_EE_IMAGE"); img != "" {
		return img
	}
	return "stratt-ee:dev"
}

// stageKey copies the dev private key somewhere it can hold 0600.
//
// The repo lives on a mount that reports 0777 for every file and cannot be chmod-ed, and OpenSSH
// refuses a world-readable private key outright ("Permissions 0777 for '/key' are too open ... This
// private key will be ignored"), which then surfaces as `Permission denied (publickey)` — an
// authentication error for what is really a file-mode problem. Staging it into a normal filesystem
// keeps the proof about the CONTENT rather than about where the checkout happens to sit.
func stageKey(t *testing.T, src string) string {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("reading the dev key: %v", err)
	}
	// The EE runs as uid 1000 and so does this test's user, so 0600 is readable in the container.
	dst := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		t.Fatalf("staging the dev key: %v", err)
	}
	return dst
}

// runPlay executes the shipped playbook out of the repo's content root, through the real EE image,
// against the node container — the same play, the same variables the Workflow declares, no fixture.
func runPlay(t *testing.T, repo, key, net, node, playbook string, extra []string) string {
	t.Helper()
	args := []string{
		"run", "--rm", "--network", net,
		"-v", filepath.Join(repo, "plugins", "ansible", "estate") + ":/project:ro",
		"-v", key + ":/key:ro",
		"-e", "ANSIBLE_HOST_KEY_CHECKING=False",
		"-e", "ANSIBLE_STDOUT_CALLBACK=default",
		"--entrypoint", "ansible-playbook",
		// Overridable so the SAME proof can be pointed at another EE — which is how the platform
		// floor was falsified: rebuilt with `--build-arg EE_CONTENT=` and re-run, apache fails on
		// Alpine again with the community.general.apk dispatch error, so the floor is demonstrably
		// what fixed it. It is also the honest way for an operator to check their own variant.
		eeImage(),
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
