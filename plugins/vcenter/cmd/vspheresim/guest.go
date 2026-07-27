// Package main is vspheresim — a vCenter API simulator whose VMs have REAL guests.
//
// EXTRACTION INTENT. This binary imports govmomi and the standard library, and
// NOTHING from Stratt. It knows no Entity, no Facet, no plugin port. That is
// deliberate: a simulator is only trustworthy as a stand-in if it is a property of
// the SUBSTRATE and not of the thing under test — and it makes lifting this into a
// standalone simulators project a `git mv` plus a go.mod. Keep it that way; an
// import of anything Stratt-shaped is the signal that a behaviour has leaked from
// the system under test into its own test double.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"
)

// The govmomi simulator already knows how to back a VM with a real Docker
// container: a VM whose Config.ExtraConfig carries RUN.container gets one, and the
// simulator then syncs the container's real IP, hostname and power state back onto
// vm.Guest. What it does NOT do is decide that a VM should have one.
//
// That decision is what this file owns, and where it is made is the whole design.
// It belongs to the SIMULATOR, never to the client: if the caller had to ask for a
// container-backed VM, then the declarations that work against vspheresim would
// differ from the ones that work against a real vCenter, and the simulator would be
// testing the wrong thing. A real hypervisor is not told to give a VM a guest OS.
// So a client creates an ordinary VM with ordinary vSphere fields, and the guest
// appears — exactly as it would on hardware.
const (
	containerBackingKey = "RUN.container"
	containerNetworkKey = "RUN.network"
	containerDMIKey     = "RUN.mountdmi"
)

// GuestBacking gives every VM a real container guest, shortly after it appears.
//
// WHY A RECONCILER RATHER THAN AN INTERCEPT. The container is created inside
// applyExtraConfig, so the backing has to arrive as a ReconfigVM — which cannot be
// done from inside the registry's own create path without re-entering locks the
// create is holding. Watching instead of intercepting sidesteps that entirely, and
// the simulator's own code makes it safe: reconfiguring a VM that is ALREADY
// powered on retroactively starts its container ("check to see if the VM is already
// powered on - if so we need to retroactively hit that path here").
//
// It is also more honest. A real VM is not reachable the instant its create call
// returns — it boots. A watcher reproduces that: the VM exists first with no guest,
// and the guest arrives after. Anything consuming a reachability coordinate has to
// tolerate "built, not yet reachable", and against this simulator it genuinely does
// rather than being handed a fully-formed guest synchronously.
type GuestBacking struct {
	// Image is the container image each VM's guest runs. Empty disables backing
	// entirely, which is the honest default for a simulator that cannot reach a
	// registry — a sim that silently produced no guests would look identical to one
	// whose image name was wrong.
	//
	// THE IMAGE MUST RUN SOMETHING LONG-LIVED, exactly as a real guest OS does. A bare
	// `alpine` starts, finds nothing to do, and exits 0 — after which the VM has a
	// backing and no guest, which reads identically to backing that never worked. Use an
	// image with a service entrypoint (an sshd image is the usual choice) or supply Args.
	Image string
	// Args are appended after the image — the guest's command. `sleep infinity` is
	// enough to keep an otherwise-idle base image alive.
	Args []string
	// Domain, when set, gives each guest the hostname `<vm-name>.<domain>` so it
	// reports a DOTTED FQDN.
	//
	// This is what lets the simulator exercise the name-first path at all. Docker
	// defaults a container's hostname to its own id, which is not dotted — so a client
	// that prefers a name and falls back to an address (ADR-0143 D1) would always take
	// the fallback here, and the name branch would go permanently untested against this
	// simulator while looking fine.
	Domain string
	// Network is an optional Docker network to attach guests to. It is how a guest
	// is made reachable from wherever the client runs, and it is a property of the
	// harness rather than of vSphere.
	Network string
	// MountDMI asks the simulator to bind-mount a synthetic SMBIOS table into the
	// guest at /sys/class/dmi/id, so the guest's own hardware UUID matches the VM's.
	// Good fidelity, and OFF by default because it does not work everywhere: the mount
	// target is under /sys, which is read-only in many container runtimes (a
	// devcontainer, docker-in-docker, most CI), and the failure mode is a container
	// that is CREATED and then refuses to start —
	//
	//   error mounting ... to rootfs at "/sys/class/dmi/id": read-only file system
	//
	// which surfaces as "the VM has no guest" and sends the reader hunting in entirely
	// the wrong place. Defaulting it off means the simulator works out of the box and
	// the fidelity is opt-in where the host supports it.
	MountDMI bool
	// All backs EVERY VM, including the ones the model pre-creates at startup.
	//
	// Off by default, and the reason is cost with a sharp edge: a model sized like a
	// small estate (-vms 25 across a few resource pools) would launch a hundred
	// containers the moment the simulator boots, most of them standing in for an
	// inventory nobody is going to touch. The default instead gives a guest to every
	// VM CREATED AFTER STARTUP — which is exactly the set a client provisioned, so
	// anything you build is reachable and the seeded furniture is not.
	//
	// The limitation that buys is worth stating: a pre-seeded VM has no coordinate, so
	// converging onto the simulated existing estate needs -guest-all and the container
	// budget that implies. It is a predictable line ("what you provisioned has a
	// guest") rather than an arbitrary one, which a cap would have been.
	All      bool
	Interval time.Duration
	Log      *slog.Logger
	// warned de-duplicates the dead-guest warning so a 2s reconcile does not produce a
	// wall of identical lines, which is its own way of hiding a signal.
	warned map[string]bool
	// preexisting are the VMs the model created before the first reconcile; skipped
	// unless All.
	preexisting map[types.ManagedObjectReference]bool
}

// Run reconciles until ctx is cancelled.
func (g GuestBacking) Run(ctx context.Context, m *simulator.Model) {
	g.warned = map[string]bool{}
	if g.Image == "" {
		g.Log.Info("guest backing disabled (no image configured); VMs will have no guest OS")
		return
	}
	g.preexisting = map[types.ManagedObjectReference]bool{}
	if !g.All {
		// Snapshot BEFORE the first tick: anything present now is the model's seeded
		// inventory, and everything after it is something a client built.
		for _, e := range m.Map().All("VirtualMachine") {
			g.preexisting[e.Reference()] = true
		}
		g.Log.Info("guest backing scoped to provisioned VMs", "preexisting", len(g.preexisting), "hint", "-guest-all backs the seeded inventory too")
	}
	t := time.NewTicker(g.Interval)
	defer t.Stop()
	g.Log.Info("guest backing enabled", "image", g.Image, "network", g.Network, "interval", g.Interval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.reconcileOnce(m)
		}
	}
}

// reconcileOnce gives a guest to every VM that lacks one. Idempotent by
// construction: a VM already carrying the backing key is skipped, so a VM is
// reconfigured exactly once however often this runs.
func (g GuestBacking) reconcileOnce(m *simulator.Model) {
	for _, e := range m.Map().All("VirtualMachine") {
		vm, ok := e.(*simulator.VirtualMachine)
		if !ok || !g.needsGuest(vm) {
			continue
		}
		if err := g.attach(m, vm); err != nil {
			// Never fatal. One VM whose guest could not start (an image that is not
			// pulled, a docker socket that is not mounted) must not stop the simulator
			// serving the API or backing every other VM — but it must be VISIBLE,
			// because a silent absence here looks exactly like a client that failed to
			// read the guest info.
			g.Log.Error("guest backing failed", "vm", vm.Name, "err", err)
			continue
		}
		g.Log.Info("guest attached", "vm", vm.Name, "image", g.Image)
	}
	g.warnDeadGuests(m)
}

// warnDeadGuests reports VMs that HAVE a backing and still no guest address.
//
// Without this the most likely misconfiguration is invisible: an image that exits
// immediately produces a VM with backing attached, no guest IP, and not one line of
// output saying so — which looks exactly like a client failing to read guest info,
// and sends the reader to the wrong side of the wire. A simulator that hides its own
// failure is worse than no simulator, because it is trusted.
func (g GuestBacking) warnDeadGuests(m *simulator.Model) {
	for _, e := range m.Map().All("VirtualMachine") {
		vm, ok := e.(*simulator.VirtualMachine)
		if !ok || vm.Config == nil || !hasBacking(vm) {
			continue
		}
		if vm.Guest != nil && vm.Guest.IpAddress != "" {
			delete(g.warned, vm.Name)
			continue
		}
		if g.warned[vm.Name] {
			continue
		}
		g.warned[vm.Name] = true
		g.Log.Warn("guest has a backing but reports no address — the container probably exited; "+
			"a guest image must run a long-lived process (check: docker ps -a --filter name=vcsim-)",
			"vm", vm.Name, "image", g.Image)
	}
}

// needsGuest decides whether one VM should be given a guest. Split out of the
// reconcile so the rule is testable without a docker daemon: everything else in
// this file's write path ends in a real container.
func (g GuestBacking) needsGuest(vm *simulator.VirtualMachine) bool {
	// Idempotence lives here: a VM already carrying the backing key is skipped, so a
	// VM is reconfigured exactly once however often the reconcile runs.
	return vm.Config != nil && !hasBacking(vm) && !g.preexisting[vm.Self]
}

func hasBacking(vm *simulator.VirtualMachine) bool {
	for _, opt := range vm.Config.ExtraConfig {
		if opt.GetOptionValue().Key == containerBackingKey {
			return true
		}
	}
	return false
}

// attach reconfigures one VM with the container backing. The RUN.container value is
// a JSON argv array — docker options followed by the image — which is the form the
// simulator parses.
func (g GuestBacking) attach(m *simulator.Model, vm *simulator.VirtualMachine) error {
	// args[0] is "opts and image": the simulator joins the whole docker command line
	// into one string and hands it to `bash -c`, so docker options may be prefixed onto
	// the image. That same join is why anything interpolated here MUST be validated —
	// the VM name is chosen by the CLIENT, and an unvalidated name would be shell
	// injection into the host running the simulator. hostnameSafe is the gate; a name
	// that does not pass simply gets no --hostname rather than a sanitised guess.
	argv, err := json.Marshal(append([]string{g.optsAndImage(vm.Name)}, g.Args...))
	if err != nil {
		return err
	}
	extra := []types.BaseOptionValue{
		&types.OptionValue{Key: containerBackingKey, Value: string(argv)},
		// Always stated explicitly rather than left to the simulator's default, which is
		// true — see MountDMI for why that default cannot be relied on.
		&types.OptionValue{Key: containerDMIKey, Value: strconv.FormatBool(g.MountDMI)},
	}
	if g.Network != "" {
		extra = append(extra, &types.OptionValue{Key: containerNetworkKey, Value: g.Network})
	}
	ctx := simulator.NewContext()
	ctx.Map = m.Map()
	vm.ReconfigVMTask(ctx, &types.ReconfigVM_Task{
		This: vm.Self,
		Spec: types.VirtualMachineConfigSpec{ExtraConfig: extra},
	})
	return nil
}

// optsAndImage builds args[0] — the simulator's "pass-through options plus the image"
// slot. Split out of attach so the exact string is unit-testable: it crosses into a
// shell, and a boundary nothing asserts is a boundary nobody has checked.
func (g GuestBacking) optsAndImage(vmName string) string {
	if g.Domain != "" && hostnameSafe(vmName) {
		return "--hostname " + strings.ToLower(vmName) + "." + g.Domain + " " + g.Image
	}
	return g.Image
}

// hostnameSafe reports whether a VM name may be interpolated into the container
// command line as a hostname label (RFC 1123, lowercased).
//
// Deliberately an allowlist rather than an escape: the value crosses into a shell,
// and the only safe thing to do with client-controlled input at that boundary is to
// refuse anything that is not obviously fine.
func hostnameSafe(name string) bool {
	return hostnameLabel.MatchString(strings.ToLower(name))
}

var hostnameLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
