// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package vmm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-logr/logr"
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
	Logger             logr.Logger

	DetachVms bool
}

func NewManager(paths host.Paths, opts ManagerOptions) *Manager {
	return &Manager{
		vms:  make(map[string]*client.ClientWithResponses),
		idMu: utilssync.NewMutexMap[string](),

		paths:              paths,
		cloudHypervisorBin: opts.CloudHypervisorBin,
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

	log.V(2).Info("Checking if vmm socket is active", "path", apiSocket)
	active, err := isSocketActive(apiSocket)
	if err != nil {
		return fmt.Errorf("error checking if %s is a active socket: %w", apiSocket, err)
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
	res, err := apiClient.GetVmInfoWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get vm: %w", err)
	}

	if res.StatusCode() == 500 && string(res.Body) == "VM is not created" {
		return nil, ErrVmNotCreated
	}

	return res.JSON200, nil
}

func (m *Manager) CreateVM(ctx context.Context, machineId string) error {
	m.idMu.Lock(machineId)
	defer m.idMu.Unlock(machineId)

	log := m.log.WithValues("machineID", machineId)

	apiClient, found := m.vms[machineId]
	if !found {
		return ErrNotFound
	}

	log.V(2).Info("Getting vm")
	_, err := apiClient.CreateVMWithResponse(ctx, client.CreateVMJSONRequestBody{
		Balloon:      nil,
		Console:      nil,
		Cpus:         nil,
		DebugConsole: nil,
		Devices:      nil,
		Disks: &[]client.DiskConfig{
			{
				Id:   ptr.To("a"),
				Path: "",
			},
		},
		Fs:              nil,
		Iommu:           nil,
		LandlockEnable:  nil,
		LandlockRules:   nil,
		Memory:          nil,
		Net:             nil,
		Numa:            nil,
		Payload:         client.PayloadConfig{},
		PciSegments:     nil,
		Platform:        nil,
		Pmem:            nil,
		Pvpanic:         nil,
		RateLimitGroups: nil,
		Rng:             nil,
		Serial:          nil,
		SgxEpc:          nil,
		Tpm:             nil,
		Vdpa:            nil,
		Vsock:           nil,
		Watchdog:        nil,
	})
	if err != nil {
		return fmt.Errorf("failed to get vm: %w", err)
	}

	return nil
}
