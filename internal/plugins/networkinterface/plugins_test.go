// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package networkinterface

import (
	"net/netip"
	"testing"

	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	plugin "github.com/ironcore-dev/provider-utils/plugin"
)

func TestToPluginSpec(t *testing.T) {
	spec := &api.NetworkInterfaceSpec{
		Metadata: &api.NetworkInterfaceMetadata{
			ID:        "id",
			Name:      "name",
			Namespace: "namespace",
		},
		Name:      "nic",
		NetworkId: "network-id",
		Ips:       []string{"192.0.2.10"},
	}

	got := ToPluginSpec(spec, "guest.example")
	if got.Hostname != "guest.example" {
		t.Fatalf("hostname = %q, want %q", got.Hostname, "guest.example")
	}
	if got.Metadata == nil || got.Metadata.ID != "id" || got.Metadata.Name != "name" || got.Metadata.Namespace != "namespace" {
		t.Fatalf("metadata was not converted: %#v", got.Metadata)
	}
}

func TestFromPluginStatusPCI(t *testing.T) {
	status, err := FromPluginStatus("nic", &plugin.NetworkInterfaceStatus{
		Handle:   "handle",
		State:    plugin.NetworkInterfaceStateReady,
		Prefixes: netip.MustParsePrefix("192.0.2.0/24"),
		Ips:      []netip.Addr{netip.MustParseAddr("192.0.2.10")},
		PCI:      &plugin.PCI{BDF: "0000:00:01.0"},
	})
	if err != nil {
		t.Fatalf("FromPluginStatus() error = %v", err)
	}

	if status.Type != api.NetworkInterfacePCIType {
		t.Fatalf("type = %q, want %q", status.Type, api.NetworkInterfacePCIType)
	}
	if status.Path != "/sys/bus/pci/devices/0000:00:01.0" {
		t.Fatalf("path = %q", status.Path)
	}
	if status.State != api.NetworkInterfaceStatePrepared {
		t.Fatalf("state = %q, want %q", status.State, api.NetworkInterfaceStatePrepared)
	}
}

func TestFromPluginStatusTAP(t *testing.T) {
	status, err := FromPluginStatus("nic", &plugin.NetworkInterfaceStatus{
		State: plugin.NetworkInterfaceStateReady,
		TAP:   &plugin.TAP{Name: "tap0"},
	})
	if err != nil {
		t.Fatalf("FromPluginStatus() error = %v", err)
	}

	if status.Type != api.NetworkInterfaceTAPType || status.Path != "tap0" {
		t.Fatalf("device = (%q, %q), want (%q, %q)", status.Type, status.Path, api.NetworkInterfaceTAPType, "tap0")
	}
}

func TestFromPluginStatusRejectsMultipleDevices(t *testing.T) {
	_, err := FromPluginStatus("nic", &plugin.NetworkInterfaceStatus{
		State: plugin.NetworkInterfaceStateReady,
		PCI:   &plugin.PCI{BDF: "0000:00:01.0"},
		TAP:   &plugin.TAP{Name: "tap0"},
	})
	if err == nil {
		t.Fatal("FromPluginStatus() error = nil, want an error")
	}
}
