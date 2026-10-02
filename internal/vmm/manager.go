// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package vmm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/cloud-hypervisor/client"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	utilssync "github.com/ironcore-dev/provider-utils/storeutils/sync"
	"k8s.io/utils/ptr"
)

const socketReadyTimeout = 10 * time.Second
const terminateGracePeriod = 5 * time.Second

type ManagerOptions struct {
	ChBinaryPath string
	FirmwarePath string
}

func NewManager(log logr.Logger, paths host.Paths, opts ManagerOptions) (*Manager, error) {
	return &Manager{
		idMu:         utilssync.NewMutexMap[string](),
		instances:    make(map[string]*client.ClientWithResponses),
		paths:        paths,
		chBinaryPath: opts.ChBinaryPath,
		firmwarePath: opts.FirmwarePath,
		log:          log,
	}, nil
}

type Manager struct {
	log logr.Logger

	idMu        *utilssync.MutexMap[string]
	instancesMu sync.RWMutex
	instances   map[string]*client.ClientWithResponses

	paths        host.Paths
	chBinaryPath string
	firmwarePath string
}

func (m *Manager) getInstance(machineID string) (*client.ClientWithResponses, bool) {
	m.instancesMu.RLock()
	defer m.instancesMu.RUnlock()
	apiClient, ok := m.instances[machineID]
	return apiClient, ok
}

func (m *Manager) setInstance(machineID string, apiClient *client.ClientWithResponses) {
	m.instancesMu.Lock()
	defer m.instancesMu.Unlock()
	m.instances[machineID] = apiClient
}

func (m *Manager) deleteInstance(machineID string) {
	m.instancesMu.Lock()
	defer m.instancesMu.Unlock()
	delete(m.instances, machineID)
}

var (
	ErrBrokenSocket = errors.New("broken socket")
	ErrNotFound     = errors.New("not found")
	ErrVmNotCreated = errors.New("vm is not created")
)

type VMState string

const (
	VMStateCreated VMState = "Created"
	VMStateRunning VMState = "Running"
	VMStateShutoff VMState = "Shutoff"
	VMStatePaused  VMState = "Paused"
)

type VMStatus struct {
	State             VMState
	Disks             []string
	NetworkInterfaces []string
}

func (m *Manager) ensureVMM(ctx context.Context, machineID string) error {
	log := m.log.WithValues("machineID", machineID)

	if apiClient, ok := m.getInstance(machineID); ok {
		if _, err := apiClient.GetVmmPing(ctx); err == nil {
			return nil
		}
		log.V(1).Info("Tracked client is stale, dropping")
		m.deleteInstance(machineID)
	}

	sockPath := m.paths.MachineChSocket(machineID)
	if apiClient, err := NewUnixSocketClient(sockPath); err == nil {
		if _, err := apiClient.GetVmmPing(ctx); err == nil {
			log.V(1).Info("Adopted running cloud-hypervisor", "socketPath", sockPath)
			m.setInstance(machineID, apiClient)
			return nil
		}
	}

	if pid, ok := m.readPid(machineID); ok && processAlive(pid) {
		return fmt.Errorf("cloud-hypervisor pid %d alive but socket %s not responding", pid, sockPath)
	}

	apiClient, err := m.launch(ctx, machineID)
	if err != nil {
		return err
	}
	m.setInstance(machineID, apiClient)
	return nil
}

func (m *Manager) adopt(ctx context.Context, machineID string) bool {
	if _, ok := m.getInstance(machineID); ok {
		return true
	}
	sockPath := m.paths.MachineChSocket(machineID)
	apiClient, err := NewUnixSocketClient(sockPath)
	if err != nil {
		return false
	}
	if _, err := apiClient.GetVmmPing(ctx); err != nil {
		return false
	}
	m.log.V(1).Info("Adopted running cloud-hypervisor", "machineID", machineID, "socketPath", sockPath)
	m.setInstance(machineID, apiClient)
	return true
}

func (m *Manager) launch(ctx context.Context, machineID string) (*client.ClientWithResponses, error) {
	log := m.log.WithValues("machineID", machineID)

	sockPath := m.paths.MachineChSocket(machineID)
	logPath := m.paths.MachineChLog(machineID)

	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to remove stale socket: %w", err)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open cloud-hypervisor log: %w", err)
	}
	defer func() {
		_ = logFile.Close() // the child keeps its own inherited fd
	}()

	cmd := exec.Command(m.chBinaryPath, "--api-socket", sockPath, "-v")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Own process group, no Pdeathsig: the process must outlive the provider.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start cloud-hypervisor: %w", err)
	}
	pid := cmd.Process.Pid
	log.V(1).Info("Started cloud-hypervisor", "pid", pid, "socketPath", sockPath)

	if err := m.writePid(machineID, pid); err != nil {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		return nil, fmt.Errorf("failed to write pidfile: %w", err)
	}

	go func() {
		werr := cmd.Wait()
		log.Info("cloud-hypervisor exited", "pid", pid, "err", werr)
	}()

	apiClient, err := NewUnixSocketClient(sockPath)
	if err != nil {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		return nil, err
	}

	waitCtx, cancel := context.WithTimeout(ctx, socketReadyTimeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := apiClient.GetVmmPing(waitCtx); err == nil {
			log.V(2).Info("cloud-hypervisor is ready", "pid", pid)
			return apiClient, nil
		}
		select {
		case <-waitCtx.Done():
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = os.Remove(sockPath)
			_ = m.removePid(machineID)
			return nil, fmt.Errorf("cloud-hypervisor for %s not ready within %s", machineID, socketReadyTimeout)
		case <-ticker.C:
		}
	}
}

func (m *Manager) stopVMM(_ context.Context, machineID string) error {
	log := m.log.WithValues("machineID", machineID)

	m.deleteInstance(machineID)

	pid, ok := m.readPid(machineID)
	if ok && processAlive(pid) {
		log.V(1).Info("Terminating cloud-hypervisor", "pid", pid)
		_ = syscall.Kill(-pid, syscall.SIGTERM)

		deadline := time.Now().Add(terminateGracePeriod)
		for time.Now().Before(deadline) && processAlive(pid) {
			time.Sleep(100 * time.Millisecond)
		}
		if processAlive(pid) {
			log.V(1).Info("Grace period elapsed, sending SIGKILL", "pid", pid)
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}

	if err := os.Remove(m.paths.MachineChSocket(machineID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove socket: %w", err)
	}
	if err := m.removePid(machineID); err != nil {
		return fmt.Errorf("failed to remove pidfile: %w", err)
	}
	return nil
}

func wrapIfSocketClosed(err error) error {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("%w: %w", ErrBrokenSocket, err)
	}
	return err
}

func (m *Manager) Status(ctx context.Context, machineID string) (*VMStatus, error) {
	m.idMu.Lock(machineID)
	defer m.idMu.Unlock(machineID)

	if !m.adopt(ctx, machineID) {
		return nil, ErrNotFound
	}

	info, err := m.getVM(ctx, machineID)
	if err != nil {
		return nil, err
	}

	status := &VMStatus{
		State: mapVMState(info.State),
	}
	for _, disk := range ptr.Deref(info.Config.Disks, nil) {
		if id := ptr.Deref(disk.Id, ""); id != "" {
			status.Disks = append(status.Disks, id)
		}
	}
	for _, dev := range ptr.Deref(info.Config.Devices, nil) {
		if name := getNicName(ptr.Deref(dev.Id, "")); name != "" {
			status.NetworkInterfaces = append(status.NetworkInterfaces, name)
		}
	}
	return status, nil
}

func mapVMState(state client.VmState) VMState {
	switch state {
	case client.Running:
		return VMStateRunning
	case client.Paused:
		return VMStatePaused
	case client.Created:
		return VMStateCreated
	default:
		return VMStateShutoff
	}
}

func (m *Manager) getVM(ctx context.Context, machineID string) (*client.VmInfo, error) {
	log := m.log.WithValues("machineID", machineID)

	apiClient, found := m.getInstance(machineID)
	if !found {
		return nil, ErrNotFound
	}

	log.V(2).Info("Getting vm")
	resp, err := apiClient.GetVmInfoWithResponse(ctx)
	if err != nil {
		return nil, wrapIfSocketClosed(fmt.Errorf("failed to get vm: %w", err))
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		if strings.Contains(string(resp.Body), "VM is not created") {
			return nil, ErrVmNotCreated
		}
		log.V(1).Info("Failed to get vm", "error", string(resp.Body))
		return nil, err
	}

	return resp.JSON200, nil
}

const ignitionConfigFwCfgName = "opt/com.coreos/config"

func ignitionFwCfgConfig(ignitionFilePath string) *client.FwCfgConfig {
	return &client.FwCfgConfig{
		E820:       new(false),
		Kernel:     new(false),
		Cmdline:    new(false),
		Initramfs:  new(false),
		AcpiTables: new(false),
		Items: &client.FwCfgItemList{
			ItemList: &[]client.FwCfgItem{
				{
					Name: ignitionConfigFwCfgName,
					File: &ignitionFilePath,
				},
			},
		},
	}
}

func (m *Manager) Create(ctx context.Context, machine *api.Machine) error {
	m.idMu.Lock(machine.ID)
	defer m.idMu.Unlock(machine.ID)

	log := m.log.WithValues("machineID", machine.ID)

	if err := m.ensureVMM(ctx, machine.ID); err != nil {
		return fmt.Errorf("failed to ensure cloud-hypervisor is running: %w", err)
	}

	apiClient, found := m.getInstance(machine.ID)
	if !found {
		return ErrNotFound
	}

	payload := client.PayloadConfig{
		Cmdline:   nil,
		Firmware:  new(m.firmwarePath),
		HostData:  nil,
		Igvm:      nil,
		Initramfs: nil,
		Kernel:    nil,
	}

	platform := &client.PlatformConfig{
		Uuid: new(machine.ID),
	}

	if machine.Spec.Ignition != nil {
		ignitionPath := m.paths.MachineIgnitionFile(machine.ID)
		if err := os.WriteFile(ignitionPath, machine.Spec.Ignition, 0600); err != nil {
			return fmt.Errorf("failed to write ignition file: %w", err)
		}

		if err := os.Chmod(ignitionPath, 0600); err != nil {
			return fmt.Errorf("failed to set ignition file permissions: %w", err)
		}
		payload.FwCfgConfig = ignitionFwCfgConfig(ignitionPath)
	}

	var disks []client.DiskConfig
	for _, vol := range machine.Status.VolumeStatus {
		if vol.State != api.VolumeStatePrepared {
			continue
		}

		disk := client.DiskConfig{
			Id: new(vol.Handle),
		}

		switch vol.Type {
		case api.VolumeSocketType:
			disk.VhostUser = new(true)
			disk.VhostSocket = new(vol.Path)
			disk.Readonly = new(false)
		case api.VolumeFileType:
			disk.Path = new(vol.Path)
			disk.ImageType = new(client.Raw)
		}

		disks = append(disks, disk)
	}

	var dev []client.DeviceConfig
	for _, nic := range machine.Status.NetworkInterfaceStatus {
		if nic.State != api.NetworkInterfaceStatePrepared {
			return fmt.Errorf("nic %s is not attached", nic.Name)
		}

		dev = append(dev, client.DeviceConfig{
			Id:   new(getNicID(nic.Name)),
			Path: new(nic.Path),
		})
	}

	log.V(2).Info("Creating vm")
	resp, err := apiClient.CreateVMWithResponse(ctx, client.CreateVMJSONRequestBody{
		Cpus: &client.CpusConfig{
			BootVcpus: int(machine.Spec.Cpu),
			MaxVcpus:  int(machine.Spec.Cpu),
		},
		Devices: &dev,
		Disks:   &disks,
		Memory: &client.MemoryConfig{
			Size:   machine.Spec.MemoryBytes,
			Shared: new(true),
		},
		Console: &client.ConsoleConfig{
			Mode: "Off",
		},
		Serial: &client.SerialConfig{
			Mode: client.ConsoleModeFile,
			File: new(m.paths.MachineChSerialLog(machine.ID)),
		},
		Payload:  payload,
		Platform: platform,
	})
	if err != nil {
		return wrapIfSocketClosed(fmt.Errorf("failed to create vm: %w", err))
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		log.V(1).Info("Failed to create vm", "error", string(resp.Body))
		return err
	}

	return nil
}

func (m *Manager) DetachDisk(ctx context.Context, instanceID string, handle string) error {
	m.idMu.Lock(instanceID)
	defer m.idMu.Unlock(instanceID)
	return m.removeDevice(ctx, instanceID, handle)
}

func (m *Manager) removeDevice(ctx context.Context, instanceID string, deviceID string) error {
	log := m.log.WithValues("instanceID", instanceID)

	apiClient, found := m.getInstance(instanceID)
	if !found {
		return ErrNotFound
	}

	resp, err := apiClient.PutVmRemoveDeviceWithResponse(ctx, client.PutVmRemoveDeviceJSONRequestBody{
		Id: new(deviceID),
	})
	if err != nil {
		return wrapIfSocketClosed(fmt.Errorf("failed to remove device: %w", err))
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		log.V(1).Info("Failed to remove device", "error", string(resp.Body))
		return err
	}
	log.V(1).Info("Removed device from on machine", "deviceID", deviceID)

	return nil
}

func (m *Manager) AttachNetworkInterface(ctx context.Context, instanceID string, nic *api.NetworkInterfaceStatus) error {
	m.idMu.Lock(instanceID)
	defer m.idMu.Unlock(instanceID)

	log := m.log.WithValues("instanceID", instanceID)

	if nic.State != api.NetworkInterfaceStatePrepared {
		return fmt.Errorf("nic %s is not attached", nic.Name)
	}

	apiClient, found := m.getInstance(instanceID)
	if !found {
		return ErrNotFound
	}

	resp, err := apiClient.PutVmAddDeviceWithResponse(ctx, client.DeviceConfig{
		Id:   new(getNicID(nic.Name)),
		Path: new(nic.Path),
	})
	if err != nil {
		return wrapIfSocketClosed(fmt.Errorf("failed to add device: %w", err))
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		log.V(1).Info("Failed to add nic", "error", string(resp.Body))
		return err
	}
	log.V(1).Info("Added device", "name", nic.Name)

	return nil
}

func (m *Manager) DetachNetworkInterface(ctx context.Context, instanceID string, nicName string) error {
	m.idMu.Lock(instanceID)
	defer m.idMu.Unlock(instanceID)
	return m.removeDevice(ctx, instanceID, getNicID(nicName))
}

func (m *Manager) AttachDisk(ctx context.Context, instanceID string, volume *api.VolumeStatus) error {
	m.idMu.Lock(instanceID)
	defer m.idMu.Unlock(instanceID)

	log := m.log.WithValues("instanceID", instanceID)

	if volume.State != api.VolumeStatePrepared {
		return fmt.Errorf("volume %s is not prepared", volume.Handle)
	}

	apiClient, found := m.getInstance(instanceID)
	if !found {
		return ErrNotFound
	}

	disk := client.DiskConfig{
		Id: new(volume.Handle),
	}

	switch volume.Type {
	case api.VolumeSocketType:
		disk.VhostUser = new(true)
		disk.VhostSocket = new(volume.Path)
		disk.Readonly = new(false)
	case api.VolumeFileType:
		disk.Path = new(volume.Path)
		// See Create: file-backed disks must declare their image type.
		disk.ImageType = new(client.Raw)
	}

	resp, err := apiClient.PutVmAddDiskWithResponse(ctx, disk)
	if err != nil {
		return wrapIfSocketClosed(fmt.Errorf("failed to add device: %w", err))
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		log.V(1).Info("Failed to add disk", "error", string(resp.Body))
		return err
	}
	log.V(1).Info("Added device", "diskName", volume.Handle)

	return nil
}

func (m *Manager) Start(ctx context.Context, instanceID string) error {
	m.idMu.Lock(instanceID)
	defer m.idMu.Unlock(instanceID)

	log := m.log.WithValues("instanceID", instanceID)

	apiClient, found := m.getInstance(instanceID)
	if !found {
		return ErrNotFound
	}

	resp, err := apiClient.BootVMWithResponse(ctx)
	if err != nil {
		return wrapIfSocketClosed(fmt.Errorf("failed to boot vm: %w", err))
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		log.V(1).Info("Failed to boot vm", "error", string(resp.Body))
		return err
	}
	log.V(1).Info("Powered on machine")

	return nil
}

func (m *Manager) Stop(ctx context.Context, instanceID string) error {
	m.idMu.Lock(instanceID)
	defer m.idMu.Unlock(instanceID)

	log := m.log.WithValues("instanceID", instanceID)

	apiClient, found := m.getInstance(instanceID)
	if !found {
		return ErrNotFound
	}

	resp, err := apiClient.ShutdownVMWithResponse(ctx)
	if err != nil {
		return wrapIfSocketClosed(fmt.Errorf("failed to shutdown vm: %w", err))
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		log.V(1).Info("Failed to shutdown vm", "error", string(resp.Body))
		return err
	}
	log.V(1).Info("Powered off machine")

	return nil
}

func (m *Manager) Delete(ctx context.Context, instanceID string) error {
	m.idMu.Lock(instanceID)
	defer m.idMu.Unlock(instanceID)

	log := m.log.WithValues("instanceID", instanceID)

	if apiClient, found := m.getInstance(instanceID); found {
		resp, err := apiClient.DeleteVMWithResponse(ctx)
		switch {
		case err != nil:
			log.V(1).Info("Failed to delete vm, terminating anyway", "error", err.Error())
		case validateStatus(resp.StatusCode()) != nil:
			log.V(1).Info("Failed to delete vm, terminating anyway", "error", string(resp.Body))
		default:
			log.V(1).Info("Deleted vm definition")
		}
	}

	return m.stopVMM(ctx, instanceID)
}

func getNicID(nicName string) string {
	return fmt.Sprintf("%s//%s", "NIC", nicName)
}

func getNicName(id string) string {
	parts := strings.Split(id, "//")
	if len(parts) != 2 || parts[0] != "NIC" {
		return ""
	}
	return parts[1]
}
