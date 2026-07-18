// Package vcenter is the vCenter Syncer plugin: the govmomi content-expertise
// that used to live in core/internal/connectors/vcenter, now behind the sovereign
// plugin port (ADR-0046). It maps vSphere objects to core-legible ObservedEntity
// wire values; the core-side host governs what it may write (ownership, identity
// gating, provenance). The plugin holds no graph write path.
package vcenter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vmware/govmomi/vim25/mo"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// vmProps / hostProps are the minimum property sets the Facet namespaces demand
// (§1.1: no speculative typing).
var vmProps = []string{
	"name",
	"config.uuid",
	"config.hardware.numCPU",
	"config.hardware.memoryMB",
	"config.guestId",
	"runtime.powerState",
	"runtime.connectionState",
	"runtime.host",
	"network", // the portgroups/networks the VM is attached to — the placed-in edge
	"guest.hostName",
	"guest.ipAddress",
	"guest.toolsRunningStatus",
}

var hostProps = []string{
	"name",
	"summary.hardware.uuid",
}

// networkProps is the minimum a vSphere network (portgroup/DVPG/opaque) needs to
// project as a subnet — the name; the moref (identity) rides the object's Self ref.
var networkProps = []string{"name"}

// normalizeNetwork maps one vSphere network — a standard portgroup, a distributed
// virtual portgroup, or an opaque network — to a `subnet` Entity (ADR-0059). vSphere
// is a network Source too: its portgroups co-exist in one estate with cloud subnets a
// Crossplane Source projects (ADR-0060 multi-source, each its own per-source row). The
// moref is the stable per-object identity; net.subnet carries the vSphere-known
// attributes as an owned-but-uncovered Facet (no schema ahead of a consumer, §1.1).
func normalizeNetwork(n mo.Network) (*pluginv1.ObservedEntity, error) {
	ref := n.Self.Value
	if ref == "" {
		return nil, fmt.Errorf("vcenter: network %q has no moref; cannot project without identity", n.Name)
	}
	facet, err := json.Marshal(map[string]any{
		"name":   n.Name,
		"moref":  ref,
		"kind":   n.Self.Type, // Network | DistributedVirtualPortgroup | OpaqueNetwork
		"source": "vsphere",
	})
	if err != nil {
		return nil, fmt.Errorf("vcenter: marshal facet net.subnet: %w", err)
	}
	return &pluginv1.ObservedEntity{
		Kind:         "subnet",
		IdentityKeys: map[string]string{"vcenter.network.moref": ref},
		Labels:       map[string]string{"source": "vsphere", "vcenter.name": n.Name},
		Facets:       map[string][]byte{"net.subnet": facet},
	}, nil
}

// normalizeVM maps one VirtualMachine to an ObservedEntity. The plugin emits
// dns.fqdn when the guest reports a dotted name — it is the content-expert. The
// core-side host decides whether this plugin (by tier + grant) may WRITE that
// cross-source identity scheme (ADR-0046 finding #4); the plugin only proposes.
func normalizeVM(vm mo.VirtualMachine) (*pluginv1.ObservedEntity, error) {
	if vm.Config == nil || vm.Config.Uuid == "" {
		return nil, fmt.Errorf("vcenter: vm %q has no config.uuid; cannot project without identity", vm.Name)
	}
	ids := map[string]string{"vcenter.uuid": vm.Config.Uuid}
	labels := map[string]string{"vcenter.name": vm.Name}

	vmConfig := map[string]any{
		"cpus":     vm.Config.Hardware.NumCPU,
		"memoryMB": vm.Config.Hardware.MemoryMB,
		"guestId":  vm.Config.GuestId,
	}
	vmRuntime := map[string]any{
		"powerState":      string(vm.Runtime.PowerState),
		"connectionState": string(vm.Runtime.ConnectionState),
	}
	netGuest := map[string]any{}
	if vm.Guest != nil {
		if vm.Guest.HostName != "" {
			netGuest["hostName"] = vm.Guest.HostName
			if strings.Contains(vm.Guest.HostName, ".") {
				ids["dns.fqdn"] = strings.ToLower(vm.Guest.HostName)
			}
		}
		if vm.Guest.IpAddress != "" {
			netGuest["ipAddress"] = vm.Guest.IpAddress
		}
		if vm.Guest.ToolsRunningStatus != "" {
			vmConfig["toolsRunningStatus"] = vm.Guest.ToolsRunningStatus
		}
	}

	facets := map[string][]byte{}
	for ns, doc := range map[string]any{"vm.config": vmConfig, "vm.runtime": vmRuntime} {
		raw, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("vcenter: marshal facet %s: %w", ns, err)
		}
		facets[ns] = raw
	}
	if len(netGuest) > 0 {
		raw, err := json.Marshal(netGuest)
		if err != nil {
			return nil, fmt.Errorf("vcenter: marshal facet net.guest: %w", err)
		}
		facets["net.guest"] = raw
	}

	return &pluginv1.ObservedEntity{Kind: "vm", IdentityKeys: ids, Labels: labels, Facets: facets}, nil
}

// normalizeHost maps one HostSystem to an ObservedEntity.
func normalizeHost(h mo.HostSystem) (*pluginv1.ObservedEntity, error) {
	uuid := ""
	if h.Summary.Hardware != nil {
		uuid = h.Summary.Hardware.Uuid
	}
	if uuid == "" {
		return nil, fmt.Errorf("vcenter: host %q has no hardware uuid; cannot project without identity", h.Name)
	}
	return &pluginv1.ObservedEntity{
		Kind:         "host",
		IdentityKeys: map[string]string{"vcenter.host.uuid": uuid},
		Labels:       map[string]string{"vcenter.name": h.Name},
	}, nil
}
