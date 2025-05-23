// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/cloud-hypervisor/client"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	ociImage "github.com/ironcore-dev/cloud-hypervisor-provider/internal/oci"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/osutils"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/networkinterface"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/volume"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/raw"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/vmm"
	apiutils "github.com/ironcore-dev/provider-utils/apiutils/api"
	"github.com/ironcore-dev/provider-utils/eventutils/event"
	"github.com/ironcore-dev/provider-utils/eventutils/recorder"
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
	ImageCache ociImage.Cache
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
	nics store.Store[*api.NetworkInterface],
	nicEvents event.Source[*api.NetworkInterface],
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
		nicEvents:              nicEvents,
		EventRecorder:          eventRecorder,
		imageCache:             opts.ImageCache,
		raw:                    opts.Raw,
		paths:                  opts.Paths,
		vmm:                    vmm,
		VolumePluginManager:    volumePluginManager,
		networkInterfacePlugin: nicPlugin,
		nics:                   nics,
	}, nil
}

type MachineReconciler struct {
	log   logr.Logger
	queue workqueue.TypedRateLimitingInterface[string]

	imageCache ociImage.Cache
	raw        raw.Raw

	paths host.Paths

	vmm *vmm.Manager

	VolumePluginManager    *volume.PluginManager
	networkInterfacePlugin networkinterface.Plugin

	machines      store.Store[*api.Machine]
	machineEvents event.Source[*api.Machine]
	nicEvents     event.Source[*api.NetworkInterface]

	nics store.Store[*api.NetworkInterface]
	recorder.EventRecorder
}

func (r *MachineReconciler) Start(ctx context.Context) error {
	log := r.log

	// TODO make configurable
	workerSize := 15

	r.imageCache.AddListener(ociImage.ListenerFuncs{
		HandlePullDoneFunc: func(evt ociImage.PullDoneEvent) {
			machines, err := r.machines.List(ctx)
			if err != nil {
				log.Error(err, "failed to list machine")
				return
			}

			for _, machine := range machines {
				if ptr.Deref(machine.Spec.Image, "") == evt.Ref {
					r.Eventf(machine.Metadata, corev1.EventTypeNormal, "PulledImage", "Pulled image %s", *machine.Spec.Image)
					log.V(1).Info("Image pulled: Requeue machines", "Image", evt.Ref, "Machine", machine.ID)
					r.queue.Add(machine.ID)
				}
			}
		},
	})

	machineEventHandlerRegistration, err := r.machineEvents.AddHandler(
		event.HandlerFunc[*api.Machine](func(evt event.Event[*api.Machine]) {
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

	nicEventHandlerRegistration, err := r.nicEvents.AddHandler(
		event.HandlerFunc[*api.NetworkInterface](func(evt event.Event[*api.NetworkInterface]) {
			machineID := getMachineNameFromNicID(evt.Object.ID)
			if machineID != nil {
				r.queue.Add(ptr.Deref(machineID, ""))
			}
		}))
	if err != nil {
		return err
	}
	defer func() {
		if err = r.machineEvents.RemoveHandler(nicEventHandlerRegistration); err != nil {
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

func getNicID(machineID, nicName string) string {
	return fmt.Sprintf("%s--%s--%s", "NIC", machineID, nicName)
}

func getNicName(id string) *string {
	parts := strings.Split(id, "--")
	if len(parts) != 3 {
		return nil
	}

	if parts[0] != "NIC" {
		return nil
	}

	return &parts[2]
}

func getMachineNameFromNicID(id string) *string {
	parts := strings.Split(id, "--")
	if len(parts) != 3 {
		return nil
	}

	if parts[0] != "NIC" {
		return nil
	}

	return &parts[1]
}

func (r *MachineReconciler) reconcileMachine(ctx context.Context, id string) error {
	log := logr.FromContextOrDiscard(ctx)

	log.V(2).Info("Getting machine from store", "id", id)
	machine, err := r.machines.Get(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("failed to fetch machine from store: %w", err)
		}

		return nil
	}

	if machine.DeletedAt != nil {
		return nil
	}

	if !slices.Contains(machine.Finalizers, MachineFinalizer) {
		machine.Finalizers = append(machine.Finalizers, MachineFinalizer)
		if _, err := r.machines.Update(ctx, machine); err != nil {
			return fmt.Errorf("failed to set finalizers: %w", err)
		}
		return nil
	}

	log.V(1).Info("Making machine directories")
	if err := host.MakeMachineDirs(r.paths, machine.ID); err != nil {
		return fmt.Errorf("error making machine directories: %w", err)
	}
	log.V(1).Info("Successfully made machine directories")

	if requeue, err := r.reconcileImage(ctx, log, machine); err != nil || requeue {
		return err
	}

	if err := r.vmm.InitVMM(ctx, machine.ID); err != nil {
		return fmt.Errorf("failed to init vmm: %w", err)
	}

	if err := r.vmm.Ping(ctx, machine.ID); err != nil {
		return fmt.Errorf("failed to ping vmm: %w", err)
	}

	nics := make(map[string]*api.NetworkInterface)
	for _, networkInterface := range machine.Spec.NetworkInterfaces {
		nicID := getNicID(machine.ID, networkInterface.Name)

		nic, err := r.nics.Get(ctx, nicID)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("failed to fetch nic from store: %w", err)
			}

			if networkInterface.DeletedAt != nil {
				log.V(2).Info("Network interface not found, skipping because deletion timestamp", "nicID", nicID)
				continue
			}

			log.V(2).Info("Network interface not present, create it", "nicID", nicID)
			nic, err = r.nics.Create(ctx, &api.NetworkInterface{
				Metadata: apiutils.Metadata{
					ID: nicID,
				},
				Spec: api.NetworkInterfaceSpec{
					Name:       networkInterface.Name,
					NetworkId:  networkInterface.NetworkId,
					Ips:        networkInterface.Ips,
					Attributes: networkInterface.Attributes,
				},
			})
			if err != nil {
				return fmt.Errorf("failed to create network interface: %w", err)
			}
		}

		if networkInterface.DeletedAt != nil {
			log.V(2).Info("NetworkInterface should be deleted", "nicID", nicID)
			if err := r.nics.Delete(ctx, nicID); err != nil {
				return fmt.Errorf("failed to delete network interface %s: %w", nicID, err)
			}
		}

		nics[networkInterface.Name] = nic
	}

	var updatedVolumeStatus []api.VolumeStatus
	for _, vol := range machine.Spec.Volumes {
		plugin, err := r.VolumePluginManager.FindPluginBySpec(vol)
		if err != nil {
			return fmt.Errorf("failed to find plugin: %w", err)
		}

		appliedVolume, err := plugin.Apply(ctx, vol, machine.ID)
		if err != nil {
			return fmt.Errorf("failed to apply volume: %w", err)
		}

		//TODO handle later detach volume
		updatedVolumeStatus = append(updatedVolumeStatus, *appliedVolume)
	}
	machine.Status.VolumeStatus = updatedVolumeStatus

	machine, err = r.machines.Update(ctx, machine)
	if err != nil {
		return fmt.Errorf("failed to update machine status: %w", err)
	}

	vm, err := r.vmm.GetVM(ctx, machine.ID)
	if err != nil {
		if errors.Is(err, vmm.ErrVmNotCreated) {
			log.V(1).Info("VM not created", "machine", machine.ID)

			if !r.nicsReady(nics) {
				log.V(1).Info("Not all Network Interfaces are ready")
				return nil
			}

			if err := r.vmm.CreateVM(ctx, machine, nics); err != nil {
				log.V(1).Info("Failed to create VM", "machine", machine.ID)
				return fmt.Errorf("failed to create VM: %w", err)
			}

			for _, nic := range nics {
				if err := r.addFinalizerToNIC(ctx, nic); err != nil {
					return fmt.Errorf("failed to add finalizer to NIC: %w", err)
				}
			}

			log.V(1).Info("Successfully created VM, requeue", "machine", machine.ID)
			r.queue.Add(machine.ID)
			return nil
		}
	}

	//power on & power off
	switch {
	case vm.State != client.Running:
		_ = r.vmm.PowerOn(ctx, machine.ID)
	}

	if err := r.reconcileNics(ctx, log, machine, nics, ptr.Deref(vm.Config.Devices, nil)); err != nil {
		return fmt.Errorf("failed to reconcile nics: %w", err)
	}

	log.V(1).Info("Successfully reconciled VM", "machine", machine.ID)
	return nil
}

func (r *MachineReconciler) reconcileNics(
	ctx context.Context,
	log logr.Logger,
	machine *api.Machine,
	desiredNics map[string]*api.NetworkInterface,
	currentDevices []client.DeviceConfig,
) error {
	currentNICs := sets.New[string]()

	for _, device := range currentDevices {
		deviceID := ptr.Deref(device.Id, "")
		if getNicName(deviceID) == nil {
			continue
		}

		nicName := ptr.Deref(getNicName(deviceID), "")

		if nic, ok := desiredNics[nicName]; ok {
			if nic.DeletedAt == nil {
				currentNICs.Insert(nicName)
				continue
			}

			log.V(1).Info("Deleting NIC", "device", deviceID, "nicName", nicName)
			if err := r.vmm.RemoveDevice(ctx, machine.ID, deviceID); err != nil {
				return fmt.Errorf("failed to remove nic: %w", err)
			}

			if err := r.removeFinalizerFromNIC(ctx, nic); err != nil {
				return fmt.Errorf("failed to remove finalizer from NIC: %w", err)
			}
		}
	}

	for nicName, nic := range desiredNics {
		if nic.DeletedAt != nil {
			continue
		}

		if _, ok := currentNICs[nicName]; !ok {
			if err := r.addFinalizerToNIC(ctx, nic); err != nil {
				return fmt.Errorf("failed to add finalizer to NIC: %w", err)
			}

			log.V(1).Info("Adding NIC", "device", nicName, "nicName", nicName)
			if err := r.vmm.AddNIC(ctx, machine.ID, nic); err != nil {
				return fmt.Errorf("failed to add NIC %s: %w", nicName, err)
			}
		}
	}

	return nil
}

func (r *MachineReconciler) nicsReady(nics map[string]*api.NetworkInterface) bool {
	for _, nic := range nics {
		if nic == nil {
			continue
		}

		if nic.Status.State != api.NetworkInterfaceStateAttached {
			return false
		}
	}
	return true
}

func (r *MachineReconciler) addFinalizerToNIC(ctx context.Context, nic *api.NetworkInterface) error {
	if slices.Contains(nic.Finalizers, MachineFinalizer) {
		return nil
	}

	nic.Finalizers = append(nic.Finalizers, MachineFinalizer)
	if _, err := r.nics.Update(ctx, nic); err != nil {
		return fmt.Errorf("failed to set nic finalizers: %w", err)
	}

	return nil
}

func (r *MachineReconciler) removeFinalizerFromNIC(ctx context.Context, nic *api.NetworkInterface) error {
	if !slices.Contains(nic.Finalizers, MachineFinalizer) {
		return nil
	}

	nic.Finalizers = utils.DeleteSliceElement(nic.Finalizers, MachineFinalizer)
	if _, err := r.nics.Update(ctx, nic); err != nil {
		return fmt.Errorf("failed to remove nic finalizers: %w", err)
	}

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
		if errors.Is(err, ociImage.ErrImagePulling) {
			log.V(1).Info("Image not in cache", "image", image)
			return true, nil
		}

		return false, fmt.Errorf("failed to get image from cache: %w", err)
	}

	log.V(1).Info("Image in cache", "image", image)
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
