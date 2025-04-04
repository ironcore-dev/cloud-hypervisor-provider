// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/cloud-hypervisor/client"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	ociImage "github.com/ironcore-dev/cloud-hypervisor-provider/internal/oci"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/osutils"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/volume"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/raw"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/vmm"
	"github.com/ironcore-dev/provider-utils/eventutils/event"
	"github.com/ironcore-dev/provider-utils/eventutils/recorder"
	"github.com/ironcore-dev/provider-utils/storeutils/store"
	corev1 "k8s.io/api/core/v1"
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
		machines:            machines,
		machineEvents:       machineEvents,
		EventRecorder:       eventRecorder,
		imageCache:          opts.ImageCache,
		raw:                 opts.Raw,
		paths:               opts.Paths,
		vmm:                 vmm,
		VolumePluginManager: volumePluginManager,
	}, nil
}

type MachineReconciler struct {
	log   logr.Logger
	queue workqueue.TypedRateLimitingInterface[string]

	imageCache ociImage.Cache
	raw        raw.Raw

	paths host.Paths

	vmm *vmm.Manager

	VolumePluginManager *volume.PluginManager

	machines      store.Store[*api.Machine]
	machineEvents event.Source[*api.Machine]
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

	imgEventReg, err := r.machineEvents.AddHandler(event.HandlerFunc[*api.Machine](func(evt event.Event[*api.Machine]) {
		r.queue.Add(evt.Object.ID)
	}))
	if err != nil {
		return err
	}
	defer func() {
		if err = r.machineEvents.RemoveHandler(imgEventReg); err != nil {
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

			if err := r.vmm.CreateVM(ctx, machine); err != nil {
				log.V(1).Info("Failed to create VM", "machine", machine.ID)
				return fmt.Errorf("failed to create VM: %w", err)
			}

			log.V(1).Info("Successfully created VM, requeue", "machine", machine.ID)
			r.queue.Add(machine.ID)
			return nil
		}
	}

	switch {
	case vm.State != client.Running:
		_ = r.vmm.PowerOn(ctx, machine.ID)
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
