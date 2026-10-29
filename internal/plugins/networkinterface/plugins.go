// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package networkinterface

import (
	"fmt"

	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	plugin "github.com/ironcore-dev/provider-utils/plugin"
)

type Plugin = plugin.NetworkInterface

func ToPluginSpec(spec *api.NetworkInterfaceSpec, hostname string) *plugin.NetworkInterfaceSpec {
	var metadata *plugin.NetworkInterfaceMetadata
	if spec.Metadata != nil {
		metadata = &plugin.NetworkInterfaceMetadata{
			ID:        spec.Metadata.ID,
			Name:      spec.Metadata.Name,
			Namespace: spec.Metadata.Namespace,
		}
	}

	return &plugin.NetworkInterfaceSpec{
		Metadata:  metadata,
		Name:      spec.Name,
		NetworkId: spec.NetworkId,
		Hostname:  hostname,
		Ips:       spec.Ips,
	}
}

func FromPluginStatus(name string, status *plugin.NetworkInterfaceStatus) (*api.NetworkInterfaceStatus, error) {
	if status == nil {
		return nil, fmt.Errorf("network interface plugin returned a nil status")
	}

	result := &api.NetworkInterfaceStatus{
		Name:   name,
		Handle: status.Handle,
		Ips:    make([]string, len(status.Ips)),
	}
	for i, ip := range status.Ips {
		if !ip.IsValid() {
			return nil, fmt.Errorf("network interface plugin returned invalid IP at index %d", i)
		}
		result.Ips[i] = ip.String()
	}

	switch status.State {
	case plugin.NetworkInterfaceStatePending:
		result.State = api.NetworkInterfaceStatePending
	case plugin.NetworkInterfaceStateReady:
		result.State = api.NetworkInterfaceStatePrepared
	case plugin.NetworkInterfaceStateError:
		result.State = api.NetworkInterfaceStateError
	default:
		return nil, fmt.Errorf("network interface plugin returned unknown state %q", status.State)
	}

	if status.Prefixes.IsValid() {
		result.Prefixes = []string{status.Prefixes.String()}
	}
	if status.VirtualIp.IsValid() {
		result.VirtualIP = status.VirtualIp.String()
	}

	if status.State != plugin.NetworkInterfaceStateReady {
		return result, nil
	}

	deviceCount := 0
	if status.PCI != nil {
		deviceCount++
		result.Type = api.NetworkInterfacePCIType
		result.Path = "/sys/bus/pci/devices/" + status.PCI.BDF
	}
	if status.TAP != nil {
		deviceCount++
		result.Type = api.NetworkInterfaceTAPType
		result.Path = status.TAP.Name
	}
	if status.Netdev != nil {
		deviceCount++
	}
	if deviceCount != 1 {
		return nil, fmt.Errorf("network interface plugin returned %d devices, want exactly one", deviceCount)
	}
	if status.Netdev != nil {
		return nil, fmt.Errorf("network interface plugin returned unsupported netdev device")
	}

	return result, nil
}
