// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package vmm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/cloud-hypervisor/client"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"github.com/ironcore-dev/ironcore/broker/common"
	utilssync "github.com/ironcore-dev/provider-utils/storeutils/sync"
	"k8s.io/utils/ptr"
)

const (
	DefaultSocketName = "api.sock"
)

type ManagerOptions struct {
	CloudHypervisorBin string
	FirmwarePath       string
	Logger             logr.Logger

	DetachVms bool
}

func NewManager(paths host.Paths, opts ManagerOptions) *Manager {
	return &Manager{
		vms:  make(map[string]*client.ClientWithResponses),
		idMu: utilssync.NewMutexMap[string](),

		paths:              paths,
		cloudHypervisorBin: opts.CloudHypervisorBin,
		firmwarePath:       opts.FirmwarePath,
		log:                opts.Logger,
		detachVms:          opts.DetachVms,
	}
}

type Manager struct {
	log logr.Logger

	vms  map[string]*client.ClientWithResponses
	idMu *utilssync.MutexMap[string]

	paths              host.Paths
	cloudHypervisorBin string
	firmwarePath       string

	detachVms bool
}

var (
	ErrNotFound                 = errors.New("not found")
	ErrAlreadyExists            = errors.New("already exists")
	ErrResourceVersionNotLatest = errors.New("resourceVersion is not latest")
	ErrVmInitialized            = errors.New("vm already initialized")

	ErrVmNotCreated = errors.New("vm is not created")
)

func (m *Manager) initVmm(log logr.Logger, apiSocket string) error {
	log.V(2).Info("Cleaning up any previous socket")
	if err := common.CleanupSocketIfExists(apiSocket); err != nil {
		return fmt.Errorf("error cleaning up socket: %w", err)
	}

	chCmd := []string{
		m.cloudHypervisorBin,
		"--api-socket",
		apiSocket,
		//TODO fix
		"-v",
	}

	log.V(1).Info("Start cloud-hypervisor", "cmd", chCmd)
	cmd := exec.Command(chCmd[0], chCmd[1:]...)

	if m.detachVms {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setpgid: true,
		}
	}

	cmd.Stdout = os.Stdout // Print output directly to console
	cmd.Stderr = os.Stderr // Print errors directly to console

	log.V(1).Info("Starting vmm")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to init cloud-hypervisor: %w", err)
	}

	return nil
}

func (m *Manager) InitVMM(ctx context.Context, machineId string) error {
	m.idMu.Lock(machineId)
	defer m.idMu.Unlock(machineId)

	log := m.log.WithValues("machineID", machineId)
	apiSocket := filepath.Join(m.paths.MachineDir(machineId), DefaultSocketName)

	log.V(2).Info("Checking if vmm socket is present", "path", apiSocket)
	present, err := isSocketPresent(apiSocket)
	if err != nil {
		return fmt.Errorf("error checking if %s is a socket: %w", apiSocket, err)
	}

	var active bool
	if present {
		log.V(2).Info("Checking if vmm socket is active", "path", apiSocket)
		active, err = isSocketActive(apiSocket)
		if err != nil {
			return fmt.Errorf("error checking if %s is a active socket: %w", apiSocket, err)
		}
	}

	if !present || !active {
		log.V(1).Info("VMM socket is not present, create it", "path", apiSocket)
		if err := m.initVmm(log, apiSocket); err != nil {
			return fmt.Errorf("error initializing vmm: %w", err)
		}
	}

	log.V(2).Info("Wait for socket", "path", apiSocket)
	if err := waitForSocketWithTimeout(ctx, 2*time.Second, apiSocket); err != nil {
		return fmt.Errorf("error waiting for socket: %w", err)
	}

	log.V(2).Info("Checking if client is present")
	if _, found := m.vms[machineId]; !found {
		log.V(1).Info("Client is not present, create it")
		apiClient, err := newUnixSocketClient(apiSocket)
		if err != nil {
			return fmt.Errorf("failed to init cloud-hypervisor client: %w", err)
		}

		m.vms[machineId] = apiClient
	}

	log.V(2).Info("VMM initialized")
	return nil
}

func (m *Manager) Ping(ctx context.Context, machineId string) error {
	m.idMu.Lock(machineId)
	defer m.idMu.Unlock(machineId)
	return m.ping(ctx, machineId)
}

func (m *Manager) ping(ctx context.Context, machineId string) error {
	log := m.log.WithValues("machineID", machineId)

	apiClient, found := m.vms[machineId]
	if !found {
		return ErrNotFound
	}

	ping, err := apiClient.GetVmmPingWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("failed to ping vmm: %w", err)
	}

	log.V(2).Info(
		"ping vmm",
		"version", ping.JSON200.Version,
		"pid", ptr.Deref(ping.JSON200.Pid, -1),
		"features", ptr.Deref(ping.JSON200.Features, nil),
		"build-version", ptr.Deref(ping.JSON200.BuildVersion, ""),
	)

	return nil
}

func (m *Manager) GetVM(ctx context.Context, machineId string) (*client.VmInfo, error) {
	m.idMu.Lock(machineId)
	defer m.idMu.Unlock(machineId)

	log := m.log.WithValues("machineID", machineId)

	apiClient, found := m.vms[machineId]
	if !found {
		return nil, ErrNotFound
	}

	log.V(2).Info("Getting vm")
	resp, err := apiClient.GetVmInfoWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get vm: %w", err)
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		if string(resp.Body) == "VM is not created" {
			return nil, ErrVmNotCreated
		}
		return nil, err
	}

	return resp.JSON200, nil
}

func (m *Manager) CreateVM(ctx context.Context, machine *api.Machine, nics map[string]*api.NetworkInterface) error {
	machineId := machine.ID
	m.idMu.Lock(machineId)
	defer m.idMu.Unlock(machineId)

	log := m.log.WithValues("machineID", machineId)

	apiClient, found := m.vms[machineId]
	if !found {
		return ErrNotFound
	}

	payload := client.PayloadConfig{
		Cmdline:   nil,
		Firmware:  ptr.To(m.firmwarePath),
		HostData:  nil,
		Igvm:      nil,
		Initramfs: nil,
		Kernel:    nil,
	}

	var disks []client.DiskConfig
	if ptr.Deref(machine.Spec.Image, "") != "" {
		disks = append(disks, client.DiskConfig{
			Path: ptr.To(m.paths.MachineRootFSFile(machineId)),
		})
	}

	for _, vol := range machine.Status.VolumeStatus {
		disk := client.DiskConfig{
			Id: ptr.To(vol.Handle),
		}

		switch vol.Type {
		case api.VolumeSocketType:
			disk.VhostUser = ptr.To(true)
			disk.VhostSocket = ptr.To(vol.Path)
			disk.Readonly = ptr.To(false)
		case api.VolumeFileType:
			disk.Path = ptr.To(vol.Path)
		}

		disks = append(disks, disk)
	}

	var dev []client.DeviceConfig
	for _, nic := range nics {
		if nic == nil {
			continue
		}

		if nic.Status.State != api.NetworkInterfaceStateAttached {
			return fmt.Errorf("nic %s is not attached", nic.ID)
		}

		dev = append(dev, client.DeviceConfig{
			Id:   ptr.To(nic.ID),
			Path: nic.Status.Path,
		})
	}

	log.V(2).Info("Creating vm")
	resp, err := apiClient.CreateVMWithResponse(ctx, client.CreateVMJSONRequestBody{
		Cpus: &client.CpusConfig{
			BootVcpus: int(math.Max(float64(machine.Spec.CpuMillis/1000), 1)),
			MaxVcpus:  int(math.Max(float64(machine.Spec.CpuMillis/1000), 1)),
		},
		Devices: &dev,
		Disks:   &disks,
		Memory: &client.MemoryConfig{
			Size:   machine.Spec.MemoryBytes,
			Shared: ptr.To(true),
		},
		Console: &client.ConsoleConfig{
			Mode: "Off",
		},
		Serial: &client.ConsoleConfig{
			Mode: "Tty",
		},
		Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("failed to get vm: %w", err)
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		return err
	}

	return nil
}

func (m *Manager) RemoveDevice(ctx context.Context, machineId string, deviceID string) error {
	m.idMu.Lock(machineId)
	defer m.idMu.Unlock(machineId)

	log := m.log.WithValues("machineID", machineId)

	apiClient, found := m.vms[machineId]
	if !found {
		return ErrNotFound
	}

	resp, err := apiClient.PutVmRemoveDeviceWithResponse(ctx, client.PutVmRemoveDeviceJSONRequestBody{
		Id: ptr.To(deviceID),
	})
	if err != nil {
		return fmt.Errorf("failed to remove device: %w", err)
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		return err
	}
	log.V(1).Info("Removed device from on machine", "deviceID", deviceID)

	return nil
}

func (m *Manager) AddNIC(ctx context.Context, machineId string, nic *api.NetworkInterface) error {
	m.idMu.Lock(machineId)
	defer m.idMu.Unlock(machineId)

	log := m.log.WithValues("machineID", machineId)

	if nic.Status.State != api.NetworkInterfaceStateAttached {
		return fmt.Errorf("nic %s is not attached", nic.ID)
	}

	nicName, err := getNicName(nic.ID)
	if err != nil {
		return err
	}

	apiClient, found := m.vms[machineId]
	if !found {
		return ErrNotFound
	}

	resp, err := apiClient.PutVmAddDeviceWithResponse(ctx, client.DeviceConfig{
		Id:   ptr.To(nicName),
		Path: nic.Status.Path,
	})
	if err != nil {
		return fmt.Errorf("failed to remove device: %w", err)
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		return err
	}
	log.V(1).Info("Added device", "nicName", nicName)

	return nil
}

func (m *Manager) PowerOn(ctx context.Context, machineId string) error {
	m.idMu.Lock(machineId)
	defer m.idMu.Unlock(machineId)

	log := m.log.WithValues("machineID", machineId)

	apiClient, found := m.vms[machineId]
	if !found {
		return ErrNotFound
	}

	resp, err := apiClient.BootVMWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("failed to boot vm: %w", err)
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		return err
	}
	log.V(1).Info("Powered on machine")

	return nil
}

func (m *Manager) PowerOff(ctx context.Context, machineId string) error {
	m.idMu.Lock(machineId)
	defer m.idMu.Unlock(machineId)

	log := m.log.WithValues("machineID", machineId)

	apiClient, found := m.vms[machineId]
	if !found {
		return ErrNotFound
	}

	resp, err := apiClient.ShutdownVMWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("failed to shutdown vm: %w", err)
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		return err
	}
	log.V(1).Info("Powered off machine")

	return nil
}

func getNicName(id string) (string, error) {
	parts := strings.Split(id, "--")
	if len(parts) != 3 {
		return "", errors.New("invalid nic name")
	}

	if parts[0] != "NIC" {
		return "", errors.New("invalid nic name")
	}

	return parts[2], nil
}
