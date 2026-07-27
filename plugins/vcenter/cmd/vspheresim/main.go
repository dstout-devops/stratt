package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vmware/govmomi/simulator"
)

// vspheresim serves a real vCenter SOAP API (govmomi's simulator) whose VMs are
// backed by real containers, so a client can create a VM through ordinary vSphere
// calls and then actually reach the guest.
//
// It exists because the stock simulator stops at the API: create-vm, power, snapshot
// and migrate all execute and the inventory is real, but no guest OS boots — so
// anything that provisions a machine and then CONFIGURES it cannot be exercised
// end to end. The container backing closes that, and it is govmomi's own feature;
// what this binary adds is deciding that a VM should have a guest, which the
// simulator leaves to its embedder (see guest.go).
func main() {
	var (
		listen   = flag.String("listen", envOr("VSPHERESIM_LISTEN", "0.0.0.0:8989"), "listen address for the vCenter API")
		useTLS   = flag.Bool("tls", os.Getenv("VSPHERESIM_TLS") != "false", "serve HTTPS with a self-signed certificate, as vCenter does; -tls=false for plaintext")
		image    = flag.String("guest-image", os.Getenv("VSPHERESIM_GUEST_IMAGE"), "container image backing each VM's guest; empty disables guest backing")
		network  = flag.String("guest-network", os.Getenv("VSPHERESIM_GUEST_NETWORK"), "docker network to attach guests to")
		gargs    = flag.String("guest-args", envOr("VSPHERESIM_GUEST_ARGS", "sleep infinity"), "command run inside each guest; must be long-lived or the guest exits and the VM has no address")
		domain   = flag.String("guest-domain", os.Getenv("VSPHERESIM_GUEST_DOMAIN"), "DNS domain for guest hostnames (<vm>.<domain>); without it guests report docker's undotted container id")
		mountDMI = flag.Bool("guest-mount-dmi", os.Getenv("VSPHERESIM_GUEST_MOUNT_DMI") == "true", "bind-mount a synthetic SMBIOS table into each guest; needs a writable /sys, so off by default")
		guestAll = flag.Bool("guest-all", os.Getenv("VSPHERESIM_GUEST_ALL") == "true", "also back the VMs the model pre-creates; off by default because -vms 25 would launch a container per seeded VM")
		interval = flag.Duration("guest-interval", envDur("VSPHERESIM_GUEST_INTERVAL", 2*time.Second), "how often to look for VMs needing a guest")
		dcs      = flag.Int("datacenters", envInt("VSPHERESIM_DATACENTERS", 1), "datacenters to model")
		clusters = flag.Int("clusters", envInt("VSPHERESIM_CLUSTERS", 1), "clusters per datacenter")
		hosts    = flag.Int("hosts", envInt("VSPHERESIM_HOSTS", 2), "standalone hosts per datacenter")
		vms      = flag.Int("vms", envInt("VSPHERESIM_VMS", 0), "pre-created VMs per resource pool")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	m := simulator.VPX()
	m.Datacenter, m.Cluster, m.Host, m.Machine = *dcs, *clusters, *hosts, *vms
	// The client provisions its own VMs; a simulator pre-populated with machines it
	// then has to distinguish from the ones under test is a worse default.
	if err := m.Create(); err != nil {
		log.Error("create model", "err", err)
		os.Exit(1)
	}
	defer m.Remove()

	// Only .Host is read by NewServer.
	//
	// TLS ON BY DEFAULT, with the simulator's self-signed certificate. Two reasons,
	// and the first is the one that matters: real vCenter is HTTPS, so a plaintext
	// default would make every client's endpoint differ between this simulator and the
	// thing it stands in for — which is the same mistake as letting a client ask for a
	// container-backed VM. The second is drop-in compatibility: this replaced the
	// stock vcsim image, which serves TLS, and every existing `https://…:8989/sdk`
	// keeps working unchanged.
	//
	// The certificate means nothing (it is httptest's localhost cert) and clients must
	// dial insecure. That is a true statement about a dev simulator rather than an
	// argument for plaintext: an endpoint that differs in SCHEME from production
	// hides a whole class of client misconfiguration.
	m.Service.Listen = &url.URL{Host: *listen}
	if *useTLS {
		m.Service.TLS = new(tls.Config)
	}
	srv := m.Service.NewServer()
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go GuestBacking{Image: *image, Args: strings.Fields(*gargs), Network: *network, Domain: *domain, MountDMI: *mountDMI, All: *guestAll, Interval: *interval, Log: log}.Run(ctx, m)

	log.Info("vspheresim serving", "url", srv.URL.String(),
		"datacenters", *dcs, "clusters", *clusters, "hosts", *hosts)
	<-ctx.Done()
	log.Info("vspheresim stopping")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
