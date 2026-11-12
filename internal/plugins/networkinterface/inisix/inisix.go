// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package inisix

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	providerplugin "github.com/ironcore-dev/provider-utils/plugin"
)

const (
	PluginName = "inisix"

	commandAdd = "ADD"
	commandDel = "DEL"

	envCommand        = "INI_COMMAND"
	envMode           = "INISIX_MODE"
	envPrefixStore    = "INISIX_PREFIX_STORE"
	envVirtualStore   = "INISIX_VF_STORE"
	defaultPluginPath = "/usr/local/bin/inisix"
	defaultMode       = "tap"

	commandTimeout = 30 * time.Second
	waitDelay      = time.Second
	maxOutput      = 1 << 20
)

var pciAddressRE = regexp.MustCompile(`^[0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-9a-fA-F]$`)

type Config struct {
	PluginPath  string
	Mode        string
	PrefixStore string
	VFStore     string
}

type Plugin struct {
	host        host.Paths
	pluginPath  string
	mode        string
	prefixStore string
	vfStore     string
}

type request struct {
	Name     string `json:"name"`
	Hostname string `json:"hostname,omitempty"`
}

type addResult struct {
	Handle   string     `json:"handle"`
	TAP      *tapDevice `json:"tap"`
	VF       *vfDevice  `json:"vf"`
	Prefixes []string   `json:"prefixes"`
	IPs      []string   `json:"ips"`
}

type tapDevice struct {
	Name string `json:"name"`
}

type vfDevice struct {
	PCIAddress string `json:"pci-address"`
}

type errorResult struct {
	Error string `json:"error"`
}

func NewPlugin(paths host.Paths, cfg Config) (providerplugin.NetworkInterface, error) {
	pluginPath := cfg.PluginPath
	if pluginPath == "" {
		pluginPath = defaultPluginPath
	}

	mode := cfg.Mode
	if mode == "" {
		mode = defaultMode
	}
	if mode != "tap" && mode != "vf" {
		return nil, fmt.Errorf("unsupported inisix mode %q", mode)
	}
	if _, err := exec.LookPath(pluginPath); err != nil {
		return nil, fmt.Errorf("cannot find inisix plugin %q: %w", pluginPath, err)
	}

	return &Plugin{
		host:        paths,
		pluginPath:  pluginPath,
		mode:        mode,
		prefixStore: cfg.PrefixStore,
		vfStore:     cfg.VFStore,
	}, nil
}

func (p *Plugin) Name() string {
	return PluginName
}

func (p *Plugin) Apply(ctx context.Context, spec *providerplugin.NetworkInterfaceSpec, machineID string) (*providerplugin.NetworkInterfaceStatus, error) {
	if spec == nil {
		return nil, fmt.Errorf("network interface spec must not be nil")
	}

	if err := os.MkdirAll(p.host.MachineNetworkInterfaceDir(machineID, spec.Name), os.ModePerm); err != nil {
		return nil, err
	}

	attachmentName := AttachmentName(machineID, spec.Name)
	result := &addResult{}
	if err := p.run(ctx, commandAdd, request{
		Name:     attachmentName,
		Hostname: spec.Hostname,
	}, result); err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = p.run(context.Background(), commandDel, request{Name: attachmentName}, nil)
		}
	}()

	if result.Handle == "" {
		return nil, fmt.Errorf("inisix ADD for attachment %q returned an empty handle", attachmentName)
	}
	if len(result.Prefixes) == 0 {
		return nil, fmt.Errorf("inisix ADD for attachment %q returned no prefixes", attachmentName)
	}
	prefixes := make([]netip.Prefix, 0, len(result.Prefixes))
	for i, value := range result.Prefixes {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("inisix ADD for attachment %q returned invalid prefix at index %d %q: %w", attachmentName, i, value, err)
		}
		prefixes = append(prefixes, prefix)
	}

	ips := make([]netip.Addr, 0, len(result.IPs))
	for i, value := range result.IPs {
		ip, err := netip.ParseAddr(value)
		if err != nil {
			return nil, fmt.Errorf("inisix ADD for attachment %q returned invalid IP at index %d: %w", attachmentName, i, err)
		}
		ips = append(ips, ip)
	}

	status := &providerplugin.NetworkInterfaceStatus{
		Handle:   result.Handle,
		State:    providerplugin.NetworkInterfaceStateReady,
		Prefixes: prefixes,
		Ips:      ips,
	}
	switch {
	case result.TAP != nil && result.VF != nil:
		return nil, fmt.Errorf("inisix ADD for attachment %q returned multiple devices", attachmentName)
	case result.TAP != nil:
		if result.TAP.Name == "" {
			return nil, fmt.Errorf("inisix ADD for attachment %q returned an empty TAP name", attachmentName)
		}
		status.TAP = &providerplugin.TAP{Name: result.TAP.Name}
	case result.VF != nil:
		if !pciAddressRE.MatchString(result.VF.PCIAddress) {
			return nil, fmt.Errorf("inisix ADD for attachment %q returned invalid PCI address %q", attachmentName, result.VF.PCIAddress)
		}
		status.PCI = &providerplugin.PCI{BDF: strings.ToLower(result.VF.PCIAddress)}
	default:
		return nil, fmt.Errorf("inisix ADD for attachment %q returned no supported device", attachmentName)
	}

	cleanup = false
	return status, nil
}

func (p *Plugin) Delete(ctx context.Context, spec *providerplugin.NetworkInterfaceSpec, machineID string) error {
	if spec == nil {
		return fmt.Errorf("network interface spec must not be nil")
	}
	attachmentName := AttachmentName(machineID, spec.Name)
	if err := p.run(ctx, commandDel, request{Name: attachmentName}, nil); err != nil {
		return err
	}

	return os.RemoveAll(p.host.MachineNetworkInterfaceDir(machineID, spec.Name))
}

func AttachmentName(machineID, networkInterfaceName string) string {
	hash := sha256.Sum256([]byte(machineID + "\x00" + networkInterfaceName))
	encoding := base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)
	return "ini-" + encoding.EncodeToString(hash[:])[:11]
}

func (p *Plugin) run(ctx context.Context, command string, req request, output any) error {
	input, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encoding inisix %s request: %w", command, err)
	}

	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	stdout := &limitedBuffer{}
	stderr := &limitedBuffer{}
	cmd := exec.CommandContext(ctx, p.pluginPath)
	cmd.WaitDelay = waitDelay
	cmd.Env = os.Environ()
	cmd.Env = setEnv(cmd.Env, envCommand, command)
	cmd.Env = setEnv(cmd.Env, envMode, p.mode)
	if p.prefixStore != "" {
		cmd.Env = setEnv(cmd.Env, envPrefixStore, p.prefixStore)
	}
	if p.vfStore != "" {
		cmd.Env = setEnv(cmd.Env, envVirtualStore, p.vfStore)
	}
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		if stdout.exceeded || stderr.exceeded {
			return fmt.Errorf("inisix %s for attachment %q produced too much output", command, req.Name)
		}
		var result errorResult
		if jsonErr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &result); jsonErr == nil && result.Error != "" {
			return fmt.Errorf("inisix %s for attachment %q failed: %s", command, req.Name, result.Error)
		}
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return fmt.Errorf("inisix %s for attachment %q failed: %w (stderr: %s)", command, req.Name, err, message)
		}
		return fmt.Errorf("inisix %s for attachment %q failed: %w", command, req.Name, err)
	}
	if stdout.exceeded || stderr.exceeded {
		return fmt.Errorf("inisix %s for attachment %q produced too much output", command, req.Name)
	}
	if output == nil {
		return nil
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), output); err != nil {
		return fmt.Errorf("decoding inisix %s result for attachment %q: %w", command, req.Name, err)
	}
	return nil
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

type limitedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	remaining := maxOutput - b.Len()
	if remaining <= 0 {
		b.exceeded = true
		return len(data), nil
	}
	if len(data) > remaining {
		_, _ = b.Buffer.Write(data[:remaining])
		b.exceeded = true
		return len(data), nil
	}
	return b.Buffer.Write(data)
}
