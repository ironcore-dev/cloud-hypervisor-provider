// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package ini_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/networkinterface/ini"
	apiutils "github.com/ironcore-dev/provider-utils/apiutils/api"
)

func TestApply(t *testing.T) {
	root := t.TempDir()
	hostPaths, err := host.PathsAt(root)
	if err != nil {
		t.Fatal(err)
	}
	pluginPath, capture := fakePlugin(t, `printf '{"handle":"inisix://host/tap/isx-test","tap":{"name":"isx-test"},"prefixes":["2001:db8:1::/64"],"ips":["2001:db8:1::1"]}'`)
	plugin, err := ini.NewPlugin(pluginPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := plugin.Init(hostPaths); err != nil {
		t.Fatal(err)
	}

	machine := &api.Machine{
		Metadata: apiutils.Metadata{
			ID:     "machine-id",
			Labels: map[string]string{"machinepoollet.ironcore.dev/machine-name": "My.Machine"},
		},
		Spec: api.MachineSpec{GuestConfig: &api.MachineGuestConfig{HostName: "guest.example"}},
	}
	status, err := plugin.Apply(t.Context(), &api.NetworkInterfaceSpec{Name: "nic0"}, machine)
	if err != nil {
		t.Fatal(err)
	}
	want := &api.NetworkInterfaceStatus{
		Name: "nic0", Handle: "inisix://host/tap/isx-test", State: api.NetworkInterfaceStatePrepared,
		Type: api.NetworkInterfaceTAPType, Path: "isx-test", QueuePairs: 1,
		Prefixes: []string{"2001:db8:1::/64"}, Ips: []string{"2001:db8:1::1"},
	}
	if fmt.Sprintf("%#v", status) != fmt.Sprintf("%#v", want) {
		t.Errorf("unexpected status: got %#v, want %#v", status, want)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(capture, "stdin"))); got != fmt.Sprintf(`{"name":%q,"hostname":"guest.example"}`, ini.AttachmentName(machine.ID, "nic0")) {
		t.Errorf("unexpected request: %s", got)
	}
}

func TestDeleteRemovesDirectory(t *testing.T) {
	root := t.TempDir()
	hostPaths, err := host.PathsAt(root)
	if err != nil {
		t.Fatal(err)
	}
	pluginPath, capture := fakePlugin(t, `cat >/dev/null`)
	plugin, err := ini.NewPlugin(pluginPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := plugin.Init(hostPaths); err != nil {
		t.Fatal(err)
	}
	dir := hostPaths.MachineNetworkInterfaceDir("machine-id", "nic0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := plugin.Delete(t.Context(), "nic0", "machine-id"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("network interface directory still exists: %v", err)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(capture, "command"))); got != "DEL" {
		t.Errorf("unexpected command: %q", got)
	}
}

func TestAttachmentNameIsSafeForInisix(t *testing.T) {
	name := ini.AttachmentName("a3f9b2c1-4d5e-6f70-8192-a3b4c5d6e7f8", "network interface/0")
	if len(name) != 15 {
		t.Fatalf("attachment name length = %d, want 15: %q", len(name), name)
	}
	if !strings.HasPrefix(name, "ini-") {
		t.Fatalf("attachment name has unexpected prefix: %q", name)
	}
}

func fakePlugin(t *testing.T, body string) (string, string) {
	t.Helper()
	capture := t.TempDir()
	plugin := filepath.Join(capture, "plugin.sh")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$INI_COMMAND\" > %q\ncat > %q\n%s\n", filepath.Join(capture, "command"), filepath.Join(capture, "stdin"), body)
	if err := os.WriteFile(plugin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return plugin, capture
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
