// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package inisix

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	providerplugin "github.com/ironcore-dev/provider-utils/plugin"
)

func TestApplyTAP(t *testing.T) {
	hostPaths, err := host.PathsAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pluginPath, capture := fakePlugin(t, `if [ "$INI_COMMAND" = "ADD" ]; then printf '%s' '{"handle":"inisix://host/tap/isx-test","tap":{"name":"isx-test"},"prefixes":["2001:db8:1::/64"],"ips":["2001:db8:1::1"]}'; fi`)
	plugin, err := NewPlugin(hostPaths, Config{PluginPath: pluginPath, Mode: "tap"})
	if err != nil {
		t.Fatal(err)
	}

	status, err := plugin.Apply(t.Context(), &providerplugin.NetworkInterfaceSpec{
		Name:     "nic0",
		Hostname: "guest.example",
	}, "machine-id")
	if err != nil {
		t.Fatal(err)
	}

	if status.Handle != "inisix://host/tap/isx-test" || status.State != providerplugin.NetworkInterfaceStateReady {
		t.Fatalf("unexpected status identity: %#v", status)
	}
	if status.TAP == nil || status.TAP.Name != "isx-test" {
		t.Fatalf("unexpected TAP status: %#v", status.TAP)
	}
	if !status.Prefixes.IsValid() || status.Prefixes.String() != "2001:db8:1::/64" {
		t.Fatalf("unexpected prefix: %v", status.Prefixes)
	}
	if len(status.Ips) != 1 || status.Ips[0].String() != "2001:db8:1::1" {
		t.Fatalf("unexpected IPs: %v", status.Ips)
	}

	request := strings.TrimSpace(readFile(t, filepath.Join(capture, "stdin")))
	if request != `{"name":"ini-`+AttachmentName("machine-id", "nic0")[4:]+`","hostname":"guest.example"}` {
		t.Fatalf("unexpected request: %s", request)
	}
	if got := readFile(t, filepath.Join(capture, "mode")); got != "tap" {
		t.Fatalf("mode = %q, want tap", got)
	}
}

func TestDeleteRemovesDirectory(t *testing.T) {
	hostPaths, err := host.PathsAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pluginPath, capture := fakePlugin(t, `:`)
	plugin, err := NewPlugin(hostPaths, Config{PluginPath: pluginPath})
	if err != nil {
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
		t.Fatalf("network interface directory still exists: %v", err)
	}
	if got := readFile(t, filepath.Join(capture, "command")); got != "DEL" {
		t.Fatalf("command = %q, want DEL", got)
	}
}

func TestAttachmentName(t *testing.T) {
	name := AttachmentName("machine-id", "network interface/0")
	if len(name) != 15 || !strings.HasPrefix(name, "ini-") {
		t.Fatalf("unexpected attachment name %q", name)
	}
}

func fakePlugin(t *testing.T, body string) (string, string) {
	t.Helper()
	capture := t.TempDir()
	pluginPath := filepath.Join(capture, "plugin.sh")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$INI_COMMAND\" > %q\nprintf '%%s' \"$INISIX_MODE\" > %q\ncat > %q\n%s\n", filepath.Join(capture, "command"), filepath.Join(capture, "mode"), filepath.Join(capture, "stdin"), body)
	if err := os.WriteFile(pluginPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return pluginPath, capture
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
