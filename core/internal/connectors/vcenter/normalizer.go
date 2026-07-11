package vcenter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vmware/govmomi/vim25/mo"

	"github.com/dstout-devops/stratt/core/internal/graph"
)

// vmProps are the VirtualMachine properties the Syncer retrieves — the
// minimum demanded by the Facet namespaces this Connector ships (§1.1: no
// speculative typing).
var vmProps = []string{
	"name",
	"config.uuid",
	"config.hardware.numCPU",
	"config.hardware.memoryMB",
	"config.guestId",
	"runtime.powerState",
	"runtime.connectionState",
	"runtime.host",
	"guest.hostName",
	"guest.ipAddress",
	"guest.toolsRunningStatus",
}

// hostProps are the HostSystem properties retrieved for runs-on Relations.
var hostProps = []string{
	"name",
	"summary.hardware.uuid",
}

// normalizeVM maps one observed VirtualMachine onto the graph shape: an
// EntityUpsert with identity keys, labels, and this Connector's Facets.
// Pure function — all writes go through the Projector in the Syncer.
func normalizeVM(vm mo.VirtualMachine) (graph.EntityUpsert, error) {
	if vm.Config == nil || vm.Config.Uuid == "" {
		return graph.EntityUpsert{}, fmt.Errorf("vcenter: vm %q has no config.uuid; cannot project without identity", vm.Name)
	}

	identity := map[string]string{"vcenter.uuid": vm.Config.Uuid}
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
			// Guest-reported FQDNs correlate across Sources; only stable
			// dotted names qualify as identity.
			if strings.Contains(vm.Guest.HostName, ".") {
				identity["dns.fqdn"] = strings.ToLower(vm.Guest.HostName)
			}
		}
		if vm.Guest.IpAddress != "" {
			netGuest["ipAddress"] = vm.Guest.IpAddress
		}
		if vm.Guest.ToolsRunningStatus != "" {
			vmConfig["toolsRunningStatus"] = vm.Guest.ToolsRunningStatus
		}
	}

	facets := map[string]json.RawMessage{}
	for ns, doc := range map[string]any{
		"vm.config":  vmConfig,
		"vm.runtime": vmRuntime,
	} {
		raw, err := json.Marshal(doc)
		if err != nil {
			return graph.EntityUpsert{}, fmt.Errorf("vcenter: marshal facet %s: %w", ns, err)
		}
		facets[ns] = raw
	}
	if len(netGuest) > 0 {
		raw, err := json.Marshal(netGuest)
		if err != nil {
			return graph.EntityUpsert{}, fmt.Errorf("vcenter: marshal facet net.guest: %w", err)
		}
		facets["net.guest"] = raw
	}

	return graph.EntityUpsert{
		Kind:         "vm",
		IdentityKeys: identity,
		Labels:       labels,
		Facets:       facets,
	}, nil
}

// normalizeHost maps one observed HostSystem onto the graph shape.
func normalizeHost(h mo.HostSystem) (graph.EntityUpsert, error) {
	uuid := ""
	if h.Summary.Hardware != nil {
		uuid = h.Summary.Hardware.Uuid
	}
	if uuid == "" {
		return graph.EntityUpsert{}, fmt.Errorf("vcenter: host %q has no hardware uuid; cannot project without identity", h.Name)
	}
	return graph.EntityUpsert{
		Kind:         "host",
		IdentityKeys: map[string]string{"vcenter.host.uuid": uuid},
		Labels:       map[string]string{"vcenter.name": h.Name},
	}, nil
}
