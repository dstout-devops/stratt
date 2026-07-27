package main

import (
	"context"
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
		image    = flag.String("guest-image", os.Getenv("VSPHERESIM_GUEST_IMAGE"), "container image backing each VM's guest; empty disables guest backing")
		network  = flag.String("guest-network", os.Getenv("VSPHERESIM_GUEST_NETWORK"), "docker network to attach guests to")
		gargs    = flag.String("guest-args", envOr("VSPHERESIM_GUEST_ARGS", "sleep infinity"), "command run inside each guest; must be long-lived or the guest exits and the VM has no address")
		domain   = flag.String("guest-domain", os.Getenv("VSPHERESIM_GUEST_DOMAIN"), "DNS domain for guest hostnames (<vm>.<domain>); without it guests report docker's undotted container id")
		mountDMI = flag.Bool("guest-mount-dmi", os.Getenv("VSPHERESIM_GUEST_MOUNT_DMI") == "true", "bind-mount a synthetic SMBIOS table into each guest; needs a writable /sys, so off by default")
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

	// Only .Host is read by NewServer; plaintext by leaving TLS nil, because this is
	// a development simulator and a TLS endpoint here would only teach an operator to
	// trust a certificate that means nothing.
	m.Service.Listen = &url.URL{Host: *listen}
	srv := m.Service.NewServer()
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go GuestBacking{Image: *image, Args: strings.Fields(*gargs), Network: *network, Domain: *domain, MountDMI: *mountDMI, Interval: *interval, Log: log}.Run(ctx, m)

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
