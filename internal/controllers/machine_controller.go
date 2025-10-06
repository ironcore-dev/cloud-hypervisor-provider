// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/cloud-hypervisor/client"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/osutils"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/networkinterface"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/volume"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/raw"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/vmm"
	"github.com/ironcore-dev/provider-utils/eventutils/event"
	"github.com/ironcore-dev/provider-utils/eventutils/recorder"
	ociutils "github.com/ironcore-dev/provider-utils/ociutils/oci"
	"github.com/ironcore-dev/provider-utils/storeutils/store"
	"github.com/ironcore-dev/provider-utils/storeutils/utils"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
)

const (
	MachineFinalizer = "machine"
)

type MachineReconcilerOptions struct {
	ImageCache ociutils.Cache
	Raw        raw.Raw

	Paths host.Paths
}

func NewMachineReconciler(
	log logr.Logger,
	machines store.Store[*api.Machine],
	machineEvents event.Source[*api.Machine],
	eventRecorder recorder.EventRecorder,
	vmm *vmm.Manager,
	volumePluginManager *volume.PluginManager,
	nicPlugin networkinterface.Plugin,
	opts MachineReconcilerOptions,
) (*MachineReconciler, error) {
	if machines == nil {
		return nil, fmt.Errorf("must specify machine store")
	}

	if machineEvents == nil {
		return nil, fmt.Errorf("must specify machine events")
	}

	return &MachineReconciler{
		log: log,
		queue: workqueue.NewTypedRateLimitingQueue[string](
			workqueue.DefaultTypedControllerRateLimiter[string](),
		),
		machines:               machines,
		machineEvents:          machineEvents,
		eventRecorder:          eventRecorder,
		imageCache:             opts.ImageCache,
		raw:                    opts.Raw,
		paths:                  opts.Paths,
		vmm:                    vmm,
		VolumePluginManager:    volumePluginManager,
		networkInterfacePlugin: nicPlugin,
	}, nil
}

type MachineReconciler struct {
	log   logr.Logger
	queue workqueue.TypedRateLimitingInterface[string]

	imageCache ociutils.Cache
	raw        raw.Raw

	paths host.Paths

	vmm *vmm.Manager

	VolumePluginManager    *volume.PluginManager
	networkInterfacePlugin networkinterface.Plugin

	machines      store.Store[*api.Machine]
	machineEvents event.Source[*api.Machine]

	eventRecorder recorder.EventRecorder
}

func (r *MachineReconciler) Start(ctx context.Context) error {
	log := r.log

	// TODO make configurable
	workerSize := 15

	r.imageCache.AddListener(ociutils.ListenerFuncs{
		HandlePullDoneFunc: func(evt ociutils.PullDoneEvent) {
			machines, err := r.machines.List(ctx)
			if err != nil {
				log.Error(err, "failed to list machine")
				return
			}

			for _, machine := range machines {
				if ptr.Deref(machine.Spec.Image, "") == evt.Ref {
					r.eventRecorder.Eventf(machine.Metadata, corev1.EventTypeNormal, "PulledImage", "Pulled image %s", *machine.Spec.Image)
					log.V(1).Info("Image pulled: Requeue machines", "Image", evt.Ref, "Machine", machine.ID)
					r.queue.Add(machine.ID)
				}
			}
		},
	})

	machineEventHandlerRegistration, err := r.machineEvents.AddHandler(
		event.HandlerFunc[*api.Machine](func(evt event.Event[*api.Machine]) {
			log.V(2).Info("Machine event received", "type", evt.Type, "id", evt.Object.ID)
			r.queue.Add(evt.Object.ID)
		}))
	if err != nil {
		return err
	}
	defer func() {
		if err = r.machineEvents.RemoveHandler(machineEventHandlerRegistration); err != nil {
			log.Error(err, "failed to remove machine event handler")
		}
	}()

	var wg sync.WaitGroup
	go func() {
		<-ctx.Done()
		r.queue.ShutDown()
	}()

	for i := 0; i < workerSize; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r.processNextWorkItem(ctx, log) {
			}
		}()
	}

	wg.Wait()
	return nil
}

func (r *MachineReconciler) processNextWorkItem(ctx context.Context, log logr.Logger) bool {
	id, shutdown := r.queue.Get()
	if shutdown {
		return false
	}
	defer r.queue.Done(id)

	log = log.WithValues("machineID", id)
	ctx = logr.NewContext(ctx, log)

	if err := r.reconcileMachine(ctx, id); err != nil {
		log.Error(err, "failed to reconcile machine")
		r.queue.AddRateLimited(id)
		return true
	}

	r.queue.Forget(id)
	return true
}

func getNicName(id string) *string {
	parts := strings.Split(id, "//")
	if len(parts) != 2 {
		return nil
	}

	if parts[0] != "NIC" {
		return nil
	}

	return &parts[1]
}

func (r *MachineReconciler) getMachineState(
	ctx context.Context, machine *api.Machine,
) (client.VmInfoState, error) {
	apiSocket := ptr.Deref(machine.Spec.ApiSocketPath, "")
	vm, err := r.vmm.GetVM(ctx, apiSocket)
	if err != nil {
		if errors.Is(err, vmm.ErrVmNotCreated) || errors.Is(err, vmm.ErrNotFound) || errors.Is(err, vmm.ErrBrokenSocket) {
			return client.Shutdown, nil
		}
		return client.Shutdown, err
	}
	if vm.State == client.Running {
		return client.Running, nil
	}
	return client.Shutdown, nil
}

func getVolumeStatus(volumes []api.VolumeStatus, name string) api.VolumeStatus {
	for _, vol := range volumes {
		if vol.Name == name {
			return vol
		}
	}
	return api.VolumeStatus{
		Name:  name,
		State: api.VolumeStatePending,
	}
}

func getNICStatus(nics []api.NetworkInterfaceStatus, name string) api.NetworkInterfaceStatus {
	for _, nic := range nics {
		if nic.Name == name {
			return nic
		}
	}
	return api.NetworkInterfaceStatus{
		Name:  name,
		State: api.NetworkInterfaceStatePending,
	}
}

func (r *MachineReconciler) deleteMachine(ctx context.Context, log logr.Logger, machine *api.Machine) error {

	state, err := r.getMachineState(ctx, machine)
	if err != nil {
		return err
	}
	if state == client.Running {
		log.V(1).Info("Power machine off")
		if err := r.vmm.PowerOff(ctx, machine.ID); !errors.Is(err, vmm.ErrNotFound) {
			return fmt.Errorf("failed to power off machine: %w", err)
		}
	}

	if err := r.vmm.Delete(ctx, machine.ID); !errors.Is(err, vmm.ErrNotFound) {
		return fmt.Errorf("failed to kill VMM: %w", err)
	}

	log.V(1).Info("Delete volumes")
	for _, vol := range machine.Spec.Volumes {
		plugin, err := r.VolumePluginManager.FindPluginBySpec(vol)
		if err != nil {
			return fmt.Errorf("failed to find plugin: %w", err)
		}

		log.V(2).Info("Delete volume", "name", vol.Name, "plugin", plugin.Name())
		if err := plugin.Delete(ctx, vol.Name, machine.ID); err != nil {
			return fmt.Errorf("failed to delete volume %s: %w", vol.Name, err)
		}
	}

	log.V(1).Info("Delete NICs")
	for _, nic := range machine.Spec.NetworkInterfaces {
		log.V(2).Info("Delete NIC", "name", nic.Name)
		if err := r.networkInterfacePlugin.Delete(ctx, nic.Name, machine.ID); err != nil {
			return fmt.Errorf("failed to delete nic %s: %w", nic.Name, err)
		}
	}

	if socket := ptr.Deref(machine.Spec.ApiSocketPath, ""); socket != "" {
		r.vmm.FreeApiSocket(ctx, socket)
	}

	if err := os.RemoveAll(r.paths.MachineDir(machine.ID)); err != nil {
		return fmt.Errorf("failed to remove machine directory: %w", err)
	}
	log.V(1).Info("Removed machine directory")

	machine.Finalizers = utils.DeleteSliceElement(machine.Finalizers, MachineFinalizer)
	if _, err := r.machines.Update(ctx, machine); store.IgnoreErrNotFound(err) != nil {
		return fmt.Errorf("failed to update machine metadata: %w", err)
	}

	log.V(1).Info("Removed Finalizer. Deletion completed")

	return nil
}

func (r *MachineReconciler) reconcileVolumes(ctx context.Context, log logr.Logger, machine *api.Machine) error {
	var updatedVolumeStatus []api.VolumeStatus
	var updatedVolumeSpec []*api.VolumeSpec

	for _, vol := range machine.Spec.Volumes {

		plugin, err := r.VolumePluginManager.FindPluginBySpec(vol)
		if err != nil {
			return fmt.Errorf("failed to find plugin: %w", err)
		}

		log.V(2).Info("Reconcile volume", "name", vol.Name, "plugin", plugin.Name())

		status := getVolumeStatus(machine.Status.VolumeStatus, vol.Name)
		if vol.DeletedAt != nil {
			if status.State != api.VolumeStateAttached {
				log.V(2).Info("Delete not attached volume", "name", vol.Name)
				if err := plugin.Delete(ctx, vol.Name, machine.ID); err != nil {
					return fmt.Errorf("failed to delete volume %s: %w", vol.Name, err)
				}
				continue
			}
			log.V(2).Info("Volume attached but deletion timestamp set", "name", vol.Name)
		}

		appliedVolume, err := plugin.Apply(ctx, vol, machine.ID)
		if err != nil {
			return fmt.Errorf("failed to apply volume: %w", err)
		}
		if status.State == api.VolumeStateAttached {
			appliedVolume.State = status.State
		}
		updatedVolumeSpec = append(updatedVolumeSpec, vol)
		updatedVolumeStatus = append(updatedVolumeStatus, *appliedVolume)
		log.V(2).Info("Volume reconciled", "name", vol.Name)
	}

	machine.Spec.Volumes = updatedVolumeSpec
	machine.Status.VolumeStatus = updatedVolumeStatus

	if _, err := r.machines.Update(ctx, machine); err != nil {
		return fmt.Errorf("failed to update machine status: %w", err)
	}

	return nil
}

func (r *MachineReconciler) reconcileNics(ctx context.Context, log logr.Logger, machine *api.Machine) error {
	var updatedNICStatus []api.NetworkInterfaceStatus
	var updatedNICSpec []*api.NetworkInterfaceSpec

	plugin := r.networkInterfacePlugin
	for _, nic := range machine.Spec.NetworkInterfaces {

		log.V(2).Info("Reconcile NIC", "name", nic.Name, "plugin", plugin.Name())

		status := getNICStatus(machine.Status.NetworkInterfaceStatus, nic.Name)

		if nic.DeletedAt != nil {
			if status.State != api.NetworkInterfaceStateAttached {
				log.V(2).Info("Delete detached  NIC", "name", nic.Name)
				if err := plugin.Delete(ctx, nic.Name, machine.ID); err != nil {
					return fmt.Errorf("failed to delete NIC %s: %w", nic.Name, err)
				}
				continue
			}
			log.V(2).Info("NIC attached but deletion timestamp set", "name", nic.Name)
		}

		appliedNIC, err := plugin.Apply(ctx, nic, machine.ID)
		if err != nil {
			return fmt.Errorf("failed to apply NIC: %w", err)
		}
		if status.State == api.NetworkInterfaceStateAttached {
			appliedNIC.State = status.State
		}
		updatedNICSpec = append(updatedNICSpec, nic)
		updatedNICStatus = append(updatedNICStatus, *appliedNIC)
		log.V(2).Info("NIC reconciled", "name", nic.Name)
	}

	machine.Spec.NetworkInterfaces = updatedNICSpec
	machine.Status.NetworkInterfaceStatus = updatedNICStatus

	if _, err := r.machines.Update(ctx, machine); err != nil {
		return fmt.Errorf("failed to update machine status: %w", err)
	}

	return nil
}

// nolint: dupl
func (r *MachineReconciler) attachDetachDisks(
	ctx context.Context,
	log logr.Logger,
	machine *api.Machine,
	vm client.VmConfig,
) error {
	apiSocket := ptr.Deref(machine.Spec.ApiSocketPath, "")
	currentDevices := sets.New[string]()

	for _, dev := range ptr.Deref(vm.Disks, []client.DiskConfig{}) {
		id := dev.Id
		if id == nil {
			continue
		}
		currentDevices.Insert(ptr.Deref(id, ""))
	}

	var updatedVolumeStatus []api.VolumeStatus
	for _, vol := range machine.Spec.Volumes {
		status := getVolumeStatus(machine.Status.VolumeStatus, vol.Name)

		if vol.DeletedAt == nil {
			if !currentDevices.Has(status.Handle) {
				if status.State != api.VolumeStatePrepared {
					log.V(1).Info("Skip disk attachment: not prepared", "disk", vol.Name)
					continue
				}
				if err := r.vmm.AddDisk(ctx, apiSocket, ptr.To(status)); err != nil {
					return fmt.Errorf("failed to add disk %s: %w", vol.Name, err)
				}

				log.V(1).Info("Added disk", "disk", vol.Name)
			}
			status.State = api.VolumeStateAttached
			updatedVolumeStatus = append(updatedVolumeStatus, status)
		} else {
			if currentDevices.Has(status.Handle) {
				if err := r.vmm.RemoveDevice(ctx, apiSocket, status.Handle); err != nil {
					return fmt.Errorf("failed to remove disk %s: %w", vol.Name, err)
				}
				log.V(1).Info("Removed disk", "disk", vol.Name)

				updatedVolumeStatus = append(updatedVolumeStatus, status)
				continue
			}

			log.V(1).Info("Disk not present: Update status", "disk", vol.Name)
			status.State = api.VolumeStatePrepared
			updatedVolumeStatus = append(updatedVolumeStatus, status)
		}
	}

	machine.Status.VolumeStatus = updatedVolumeStatus
	if _, err := r.machines.Update(ctx, machine); err != nil {
		return fmt.Errorf("failed to update machine status: %w", err)
	}

	return nil
}

// nolint: dupl
func (r *MachineReconciler) attachDetachNICs(
	ctx context.Context,
	log logr.Logger,
	machine *api.Machine,
	vm client.VmConfig,
) error {
	apiSocket := ptr.Deref(machine.Spec.ApiSocketPath, "")
	currentDevices := sets.New[string]()

	for _, dev := range ptr.Deref(vm.Devices, []client.DeviceConfig{}) {
		name := getNicName(ptr.Deref(dev.Id, ""))
		if name == nil {
			continue
		}
		currentDevices.Insert(ptr.Deref(name, ""))
	}

	var updatedNICStatus []api.NetworkInterfaceStatus
	for _, nic := range machine.Spec.NetworkInterfaces {
		status := getNICStatus(machine.Status.NetworkInterfaceStatus, nic.Name)

		if nic.DeletedAt == nil {
			if !currentDevices.Has(status.Name) {
				if status.State != api.NetworkInterfaceStatePrepared {
					log.V(1).Info("Skip NIC attachment: not prepared", "nic", nic.Name)
					continue
				}

				if err := r.vmm.AddNIC(ctx, apiSocket, ptr.To(status)); err != nil {
					return fmt.Errorf("failed to add disk %s: %w", nic.Name, err)
				}

				log.V(1).Info("Added NIC", "nic", nic.Name)
			}
			status.State = api.NetworkInterfaceStateAttached
			updatedNICStatus = append(updatedNICStatus, status)
		} else {
			if currentDevices.Has(status.Name) {
				if err := r.vmm.RemoveNIC(ctx, apiSocket, nic.Name); err != nil {
					return fmt.Errorf("failed to remove NIC %s: %w", status.Name, err)
				}
				log.V(1).Info("Removed NIC", "nic", status.Name)

				updatedNICStatus = append(updatedNICStatus, status)
				r.queue.Add(machine.ID)
				continue
			}

			log.V(1).Info("NIC not present: Update status", "nic", nic.Name)
			status.State = api.NetworkInterfaceStatePrepared
			updatedNICStatus = append(updatedNICStatus, status)
		}
	}

	machine.Status.NetworkInterfaceStatus = updatedNICStatus
	if _, err := r.machines.Update(ctx, machine); err != nil {
		return fmt.Errorf("failed to update machine status: %w", err)
	}

	return nil
}

// nolint: gocyclo
func (r *MachineReconciler) reconcileMachine(ctx context.Context, id string) error {
	log := logr.FromContextOrDiscard(ctx)

	log.V(1).Info("Reconciling machine", "id", id)
	log.V(2).Info("Getting machine from store", "id", id)
	machine, err := r.machines.Get(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("failed to fetch machine from store: %w", err)
		}

		return nil
	}

	if machine.DeletedAt != nil {
		if err := r.deleteMachine(ctx, log, machine); err != nil {
			return fmt.Errorf("failed to delete machine: %w", err)
		}
		log.V(1).Info("Successfully deleted machine")
		return nil
	}

	if !slices.Contains(machine.Finalizers, MachineFinalizer) {
		machine.Finalizers = append(machine.Finalizers, MachineFinalizer)
		if _, err := r.machines.Update(ctx, machine); err != nil {
			return fmt.Errorf("failed to set finalizers: %w", err)
		}
		return nil
	}

	log.V(2).Info("Making machine directories")
	if err := host.MakeMachineDirs(r.paths, machine.ID); err != nil {
		return fmt.Errorf("error making machine directories: %w", err)
	}
	log.V(2).Info("Successfully made machine directories")

	if requeue, err := r.reconcileImage(ctx, log, machine); err != nil || requeue {
		return err
	}

	if machine.Spec.ApiSocketPath == nil {
		sock, err := r.vmm.GetFreeApiSocket()
		if err != nil {
			log.V(1).Info("Failed to get free api socket")
			//TODO
			return nil
		}
		machine.Spec.ApiSocketPath = sock
		machine, err = r.machines.Update(ctx, machine)
		if err != nil {
			return fmt.Errorf("failed to update machine status: %w", err)
		}
	}

	apiSocket := ptr.Deref(machine.Spec.ApiSocketPath, "")

	if err := r.vmm.Ping(ctx, apiSocket); err != nil {
		return fmt.Errorf("failed to ping vmm: %w", err)
	}

	if err := r.reconcileVolumes(ctx, log, machine); err != nil {
		return fmt.Errorf("failed to reconcile volumes: %w", err)
	}

	if err := r.reconcileNics(ctx, log, machine); err != nil {
		return fmt.Errorf("failed to reconcile nics: %w", err)
	}

	vm, err := r.vmm.GetVM(ctx, apiSocket)
	if err != nil {
		if !errors.Is(err, vmm.ErrVmNotCreated) {
			return fmt.Errorf("failed to get vm: %w", err)
		}

		log.V(1).Info("VM not created", "machine", machine.ID)

		if err := r.vmm.CreateVM(ctx, machine); err != nil {
			log.V(1).Info("Failed to create VM", "machine", machine.ID)
			return fmt.Errorf("failed to create VM: %w", err)
		}

		log.V(1).Info("Successfully created VM, requeue", "machine", machine.ID)
		r.queue.Add(machine.ID)
		return nil
	}

	if platform := ptr.Deref(vm.Config.Platform, client.PlatformConfig{}); ptr.Deref(platform.Uuid, "") != machine.ID {
		return fmt.Errorf("machine and vm IDs do not match")
	}

	switch machine.Spec.Power {
	case api.PowerStatePowerOn:
		if vm.State != client.Running {
			if err := r.vmm.PowerOn(ctx, apiSocket); err != nil {
				return fmt.Errorf("failed to power on VM: %w", err)
			}
		}
	case api.PowerStatePowerOff:
		if vm.State == client.Running {
			if err := r.vmm.PowerOff(ctx, apiSocket); err != nil {
				return fmt.Errorf("failed to power off VM: %w", err)
			}
		}
	}

	if err := r.attachDetachDisks(ctx, log, machine, vm.Config); err != nil {
		return fmt.Errorf("failed to attach detach disks: %w", err)
	}

	if err := r.attachDetachNICs(ctx, log, machine, vm.Config); err != nil {
		return fmt.Errorf("failed to attach detach disks: %w", err)
	}

	switch machine.Spec.Power {
	case api.PowerStatePowerOn:
		machine.Status.State = api.MachineStateRunning
	case api.PowerStatePowerOff:
		machine.Status.State = api.MachineStateTerminated
	}

	machine, err = r.machines.Update(ctx, machine)
	if err != nil {
		return fmt.Errorf("failed to update machine status: %w", err)
	}

	log.V(1).Info("Reconciled machine successfully ", "machine", machine.ID)
	return nil
}

func (r *MachineReconciler) reconcileImage(
	ctx context.Context,
	log logr.Logger,
	machine *api.Machine,
) (bool, error) {
	image := ptr.Deref(machine.Spec.Image, "")
	if image == "" {
		log.V(2).Info("No image in machine set, skip fetch")
		return false, nil
	}

	img, err := r.imageCache.Get(ctx, image)
	if err != nil {
		if errors.Is(err, ociutils.ErrImagePulling) {
			log.V(1).Info("Image not in cache", "image", image)
			return true, nil
		}

		return false, fmt.Errorf("failed to get image from cache: %w", err)
	}

	log.V(2).Info("Image in cache", "image", image)
	rootFSFile := r.paths.MachineRootFSFile(machine.ID)
	ok, err := osutils.RegularFileExists(rootFSFile)
	if err != nil {
		return false, err
	}
	if !ok {
		if err := r.raw.Create(rootFSFile, raw.WithSourceFile(img.RootFS.Path)); err != nil {
			return false, fmt.Errorf("error creating root fs disk: %w", err)
		}
		//if err := os.Chmod(rootFSFile, 0666); err != nil {
		//	return false, fmt.Errorf("error changing root fs disk mode: %w", err)
		//}
	}

	return false, nil
}
