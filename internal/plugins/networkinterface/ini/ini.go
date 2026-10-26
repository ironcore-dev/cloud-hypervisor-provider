// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package ini

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
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/networkinterface"
	machinepoolletv1alpha1 "github.com/ironcore-dev/ironcore/poollet/machinepoollet/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	PluginName  = "ini"
	execTimeout = 30 * time.Second
	waitDelay   = time.Second
	maxOutput   = 1 << 20
	envCommand  = "INI_COMMAND"
	commandAdd  = "ADD"
	commandDel  = "DEL"
)

type request struct {
	Name     string `json:"name"`
	Hostname string `json:"hostname,omitempty"`
}

type addResult struct {
	Handle   string     `json:"handle"`
	Tap      *tapDevice `json:"tap"`
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

type Plugin struct {
	pluginPath string
	host       host.Paths
}

func NewPlugin(pluginPath string) (networkinterface.Plugin, error) {
	if pluginPath == "" {
		return nil, fmt.Errorf("no ini plugin path provided")
	}
	return &Plugin{pluginPath: pluginPath}, nil
}

func (p *Plugin) Name() string { return PluginName }

func (p *Plugin) Init(host host.Paths) error {
	p.host = host
	return nil
}

func AttachmentName(machineID, networkInterfaceName string) string {
	// inisix uses the attachment name as the basis for a Linux interface name.
	// Keep the name within IFNAMSIZ and avoid passing UUIDs or arbitrary user
	// input through a plugin command line or into a device name.
	hash := sha256.Sum256([]byte(machineID + "\x00" + networkInterfaceName))
	encoded := base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)
	return "ini-" + encoded.EncodeToString(hash[:])[:11]
}

func (p *Plugin) Apply(ctx context.Context, spec *api.NetworkInterfaceSpec, machine *api.Machine) (*api.NetworkInterfaceStatus, error) {
	log := ctrl.LoggerFrom(ctx)
	if machine == nil {
		return nil, fmt.Errorf("machine must not be nil")
	}

	if err := os.MkdirAll(p.host.MachineNetworkInterfaceDir(machine.ID, spec.Name), os.ModePerm); err != nil {
		return nil, err
	}

	name := AttachmentName(machine.ID, spec.Name)
	result := &addResult{}
	if err := p.run(ctx, commandAdd, request{Name: name, Hostname: hostname(machine)}, result); err != nil {
		return nil, err
	}
	if result.Handle == "" {
		return nil, fmt.Errorf("ini plugin ADD for attachment %q returned an empty handle", name)
	}

	prefixes := make([]string, 0, len(result.Prefixes))
	for _, value := range result.Prefixes {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("ini plugin ADD for attachment %q returned invalid prefix %q: %w", name, value, err)
		}
		prefixes = append(prefixes, prefix.String())
	}
	if len(prefixes) == 0 {
		return nil, fmt.Errorf("ini plugin ADD for attachment %q returned no prefixes", name)
	}

	ips := make([]string, 0, len(result.IPs))
	for _, value := range result.IPs {
		ip, err := netip.ParseAddr(value)
		if err != nil {
			return nil, fmt.Errorf("ini plugin ADD for attachment %q returned invalid ip %q: %w", name, value, err)
		}
		ips = append(ips, ip.String())
	}

	status := &api.NetworkInterfaceStatus{
		Name:     spec.Name,
		Handle:   result.Handle,
		State:    api.NetworkInterfaceStatePrepared,
		Prefixes: prefixes,
		Ips:      ips,
	}
	switch {
	case result.Tap != nil && result.VF != nil:
		return nil, fmt.Errorf("ini plugin ADD for attachment %q returned more than one device member", name)
	case result.Tap != nil:
		if result.Tap.Name == "" {
			return nil, fmt.Errorf("ini plugin ADD for attachment %q returned a tap device without a name", name)
		}
		status.Type = api.NetworkInterfaceTAPType
		status.Path = result.Tap.Name
		status.QueuePairs = networkinterface.QueuePairsForVCPUs(int(machine.Spec.Cpu))
		log.V(1).Info("Applied INI attachment", "attachment", name, "tapName", result.Tap.Name)
	case result.VF != nil:
		if !pciAddressRE.MatchString(result.VF.PCIAddress) {
			return nil, fmt.Errorf("ini plugin ADD for attachment %q returned invalid PCI address %q", name, result.VF.PCIAddress)
		}
		status.Type = api.NetworkInterfacePCIType
		status.Path = filepath.Join("/sys/bus/pci/devices", strings.ToLower(result.VF.PCIAddress))
	default:
		return nil, fmt.Errorf("ini plugin ADD for attachment %q returned no supported device member", name)
	}
	return status, nil
}

func (p *Plugin) Delete(ctx context.Context, computeNicName, machineID string) error {
	if err := p.run(ctx, commandDel, request{Name: AttachmentName(machineID, computeNicName)}, nil); err != nil {
		return err
	}
	return os.RemoveAll(p.host.MachineNetworkInterfaceDir(machineID, computeNicName))
}

func hostname(machine *api.Machine) string {
	if machine.Spec.GuestConfig != nil && machine.Spec.GuestConfig.HostName != "" {
		return machine.Spec.GuestConfig.HostName
	}
	return sanitizeHostname(machine.Labels[machinepoolletv1alpha1.MachineNameLabel])
}

var invalidHostnameChars = regexp.MustCompile(`[^a-z0-9]+`)

func sanitizeHostname(value string) string {
	value = invalidHostnameChars.ReplaceAllString(strings.ToLower(value), "-")
	value = strings.Trim(value, "-")
	if len(value) > 63 {
		value = strings.TrimRight(value[:63], "-")
	}
	return value
}

var pciAddressRE = regexp.MustCompile(`^[0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-9a-fA-F]$`)

func (p *Plugin) run(ctx context.Context, command string, req request, out any) error {
	input, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("error encoding ini plugin request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	var stdout, stderr limitedBuffer
	cmd := exec.CommandContext(ctx, p.pluginPath)
	cmd.WaitDelay = waitDelay
	cmd.Env = append(os.Environ(), envCommand+"="+command)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stdout.exceeded || stderr.exceeded {
			return fmt.Errorf("ini plugin %s for attachment %q produced too much output", command, req.Name)
		}
		decoded := &errorResult{}
		if jsonErr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), decoded); jsonErr == nil && decoded.Error != "" {
			return fmt.Errorf("ini plugin %s for attachment %q failed: %s", command, req.Name, decoded.Error)
		}
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return fmt.Errorf("ini plugin %s for attachment %q failed: %w (stderr: %s)", command, req.Name, err, message)
		}
		return fmt.Errorf("ini plugin %s for attachment %q failed: %w", command, req.Name, err)
	}
	if stdout.exceeded || stderr.exceeded {
		return fmt.Errorf("ini plugin %s for attachment %q produced too much output", command, req.Name)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), out); err != nil {
		return fmt.Errorf("error decoding ini plugin %s result for attachment %q (stdout: %q): %w", command, req.Name, strings.TrimSpace(stdout.String()), err)
	}
	return nil
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
