// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package vmm

import (
	"context"
	b64 "encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/cloud-hypervisor/client"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	utilssync "github.com/ironcore-dev/provider-utils/storeutils/sync"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
)

type ManagerOptions struct {
	CHSocketsPath     string
	FirmwarePath      string
	ReservedInstances []string
}

func NewManager(log logr.Logger, paths host.Paths, opts ManagerOptions) (*Manager, error) {
	initLog := log.WithName("init")

	entries, err := os.ReadDir(opts.CHSocketsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read cloud-hypervisor sockets dir: %w", err)
	}

	m := &Manager{
		idMu:         utilssync.NewMutexMap[string](),
		instances:    make(map[string]*client.ClientWithResponses),
		paths:        paths,
		firmwarePath: opts.FirmwarePath,
		log:          log,
		free:         sets.New[string](),
	}
	reserved := sets.NewString(opts.ReservedInstances...)
	for _, v := range entries {
		if v.IsDir() {
			continue
		}
		if filepath.Ext(v.Name()) != ".sock" {
			continue
		}

		socketPath := filepath.Join(opts.CHSocketsPath, v.Name())

		apiClient, err := newUnixSocketClient(socketPath)
		if err != nil {
			initLog.V(1).Info("Failed to init cloud-hypervisor client", "path", socketPath)
			continue
		}

		initLog.V(2).Info("Created cloud-hypervisor client", "socketPath", socketPath)
		m.instances[socketPath] = apiClient

		if _, err := m.GetVM(context.TODO(), socketPath); errors.Is(err, ErrVmNotCreated) {
			if !reserved.Has(socketPath) {
				m.free.Insert(socketPath)
			} else {
				initLog.V(2).Info("Socket blocked and skipped", "socketPath", socketPath)
			}
		}
	}

	initLog.V(1).Info("Successfully initialized clients", "num", len(m.instances))

	return m, nil
}

type Manager struct {
	log logr.Logger

	idMu      *utilssync.MutexMap[string]
	instances map[string]*client.ClientWithResponses

	free   sets.Set[string]
	freeMu sync.Mutex

	paths        host.Paths
	firmwarePath string
}

var (
	ErrNotFound                 = errors.New("not found")
	ErrAlreadyExists            = errors.New("already exists")
	ErrResourceVersionNotLatest = errors.New("resourceVersion is not latest")
	ErrVmInitialized            = errors.New("vm already initialized")

	ErrBrokenSocket = errors.New("broken socket")

	ErrVmNotCreated = errors.New("vm is not created")
)

func (m *Manager) Ping(ctx context.Context, instanceID string) error {
	m.idMu.Lock(instanceID)
	defer m.idMu.Unlock(instanceID)
	return m.ping(ctx, instanceID)
}

func (m *Manager) ping(ctx context.Context, instanceID string) error {
	log := m.log.WithValues("instanceID", instanceID)

	apiClient, found := m.instances[instanceID]
	if !found {
		return ErrNotFound
	}

	ping, err := apiClient.GetVmmPingWithResponse(ctx)
	if err != nil {
		return wrapIfSocketClosed(fmt.Errorf("failed to ping vmm: %w", err))
	}

	if ping.JSON200 != nil {
		log.V(2).Info(
			"ping vmm",
			"version", ping.JSON200.Version,
			"pid", ptr.Deref(ping.JSON200.Pid, -1),
			"features", ptr.Deref(ping.JSON200.Features, nil),
			"build-version", ptr.Deref(ping.JSON200.BuildVersion, ""),
		)
	}

	return nil
}

func (m *Manager) GetFreeApiSocket() (*string, error) {
	m.freeMu.Lock()
	defer m.freeMu.Unlock()

	socket, found := m.free.PopAny()
	if !found {
		return nil, fmt.Errorf("no free socket available")
	}

	return ptr.To(socket), nil
}

func (m *Manager) FreeApiSocket(ctx context.Context, socket string) {
	m.freeMu.Lock()
	defer m.freeMu.Unlock()

	if err := m.ping(ctx, socket); err != nil {
		m.log.Info("Failed to ping socket: discard socket", "socket", socket)
		return
	}
	m.free.Insert(socket)
}

func wrapIfSocketClosed(err error) error {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("%w: %w", ErrBrokenSocket, err)
	}
	return err
}

func (m *Manager) GetVM(ctx context.Context, instanceID string) (*client.VmInfo, error) {
	m.idMu.Lock(instanceID)
	defer m.idMu.Unlock(instanceID)

	log := m.log.WithValues("instanceID", instanceID)

	apiClient, found := m.instances[instanceID]
	if !found {
		return nil, ErrNotFound
	}

	log.V(2).Info("Getting vm")
	resp, err := apiClient.GetVmInfoWithResponse(ctx)
	if err != nil {
		return nil, wrapIfSocketClosed(fmt.Errorf("failed to get vm: %w", err))
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		if string(resp.Body) == "Error from API: The VM info is not available: VM is not created" {
			return nil, ErrVmNotCreated
		}
		log.V(1).Info("Failed to get vm", "error", string(resp.Body))
		return nil, err
	}

	return resp.JSON200, nil
}

func (m *Manager) CreateVM(ctx context.Context, machine *api.Machine) error {
	instanceID := ptr.Deref(machine.Spec.ApiSocketPath, "")
	m.idMu.Lock(instanceID)
	defer m.idMu.Unlock(instanceID)

	log := m.log.WithValues("instanceID", instanceID)

	apiClient, found := m.instances[instanceID]
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

	platform := &client.PlatformConfig{
		Uuid: ptr.To(machine.ID),
	}

	if machine.Spec.Ignition != nil {
		platform.OemStrings = ptr.To([]string{
			b64.StdEncoding.EncodeToString(machine.Spec.Ignition),
		})
	}

	var disks []client.DiskConfig
	if ptr.Deref(machine.Spec.Image, "") != "" {
		disks = append(disks, client.DiskConfig{
			Path: ptr.To(m.paths.MachineRootFSFile(machine.ID)),
		})
	}

	for _, vol := range machine.Status.VolumeStatus {
		if vol.State != api.VolumeStatePrepared {
			continue
		}

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
	for _, nic := range machine.Status.NetworkInterfaceStatus {
		if nic.State != api.NetworkInterfaceStatePrepared {
			return fmt.Errorf("nic %s is not attached", nic.Name)
		}

		dev = append(dev, client.DeviceConfig{
			Id:   ptr.To(getNicID(nic.Name)),
			Path: nic.Path,
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
			Shared: ptr.To(true),
		},
		Console: &client.ConsoleConfig{
			Mode: "Off",
		},
		Serial: &client.ConsoleConfig{
			Mode: "Tty",
		},
		Payload:  payload,
		Platform: platform,
	})
	if err != nil {
		return wrapIfSocketClosed(fmt.Errorf("failed to get vm: %w", err))
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		log.V(1).Info("Failed to create vm", "error", string(resp.Body))
		return err
	}

	return nil
}

func (m *Manager) RemoveDevice(ctx context.Context, instanceID string, deviceID string) error {
	m.idMu.Lock(instanceID)
	defer m.idMu.Unlock(instanceID)

	log := m.log.WithValues("instanceID", instanceID)

	apiClient, found := m.instances[instanceID]
	if !found {
		return ErrNotFound
	}

	resp, err := apiClient.PutVmRemoveDeviceWithResponse(ctx, client.PutVmRemoveDeviceJSONRequestBody{
		Id: ptr.To(deviceID),
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

func (m *Manager) AddNIC(ctx context.Context, instanceID string, nic *api.NetworkInterfaceStatus) error {
	m.idMu.Lock(instanceID)
	defer m.idMu.Unlock(instanceID)

	log := m.log.WithValues("instanceID", instanceID)

	if nic.State != api.NetworkInterfaceStatePrepared {
		return fmt.Errorf("nic %s is not attached", nic.Name)
	}

	apiClient, found := m.instances[instanceID]
	if !found {
		return ErrNotFound
	}

	resp, err := apiClient.PutVmAddDeviceWithResponse(ctx, client.DeviceConfig{
		Id:   ptr.To(getNicID(nic.Name)),
		Path: nic.Path,
	})
	if err != nil {
		return wrapIfSocketClosed(fmt.Errorf("failed to remove device: %w", err))
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		log.V(1).Info("Failed to add nic", "error", string(resp.Body))
		return err
	}
	log.V(1).Info("Added device", "name", nic.Name)

	return nil
}

func (m *Manager) RemoveNIC(ctx context.Context, instanceID string, nicName string) error {
	return m.RemoveDevice(ctx, instanceID, getNicID(nicName))
}

func (m *Manager) AddDisk(ctx context.Context, instanceID string, volume *api.VolumeStatus) error {
	m.idMu.Lock(instanceID)
	defer m.idMu.Unlock(instanceID)

	log := m.log.WithValues("instanceID", instanceID)

	if volume.State != api.VolumeStatePrepared {
		return fmt.Errorf("volume %s is not prepared", volume.Handle)
	}

	apiClient, found := m.instances[instanceID]
	if !found {
		return ErrNotFound
	}

	disk := client.DiskConfig{
		Id: ptr.To(volume.Handle),
	}

	switch volume.Type {
	case api.VolumeSocketType:
		disk.VhostUser = ptr.To(true)
		disk.VhostSocket = ptr.To(volume.Path)
		disk.Readonly = ptr.To(false)
	case api.VolumeFileType:
		disk.Path = ptr.To(volume.Path)
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

func (m *Manager) PowerOn(ctx context.Context, instanceID string) error {
	m.idMu.Lock(instanceID)
	defer m.idMu.Unlock(instanceID)

	log := m.log.WithValues("instanceID", instanceID)

	apiClient, found := m.instances[instanceID]
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

func (m *Manager) PowerOff(ctx context.Context, instanceID string) error {
	m.idMu.Lock(instanceID)
	defer m.idMu.Unlock(instanceID)

	log := m.log.WithValues("instanceID", instanceID)

	apiClient, found := m.instances[instanceID]
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

	apiClient, found := m.instances[instanceID]
	if !found {
		return ErrNotFound
	}

	resp, err := apiClient.DeleteVMWithResponse(ctx)
	if err != nil {
		return wrapIfSocketClosed(fmt.Errorf("failed to delete vm: %w", err))
	}

	if err := validateStatus(resp.StatusCode()); err != nil {
		log.V(1).Info("Failed to delete vm", "error", string(resp.Body))
		return err
	}
	log.V(1).Info("Deleted machine")

	return nil
}

func getNicID(nicName string) string {
	return fmt.Sprintf("%s//%s", "NIC", nicName)
}
