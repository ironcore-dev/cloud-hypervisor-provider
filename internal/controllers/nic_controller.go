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
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/networkinterface"
	"github.com/ironcore-dev/provider-utils/eventutils/event"
	"github.com/ironcore-dev/provider-utils/eventutils/recorder"
	"github.com/ironcore-dev/provider-utils/storeutils/store"
	"github.com/ironcore-dev/provider-utils/storeutils/utils"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
)

const (
	NetworkInterfaceFinalizer = "nic"
)

type NetworkInterfaceReconcilerOptions struct {
	Paths host.Paths
}

func NewNetworkInterfaceReconciler(
	log logr.Logger,
	eventRecorder recorder.EventRecorder,
	nics store.Store[*api.NetworkInterface],
	nicEvents event.Source[*api.NetworkInterface],
	nicPlugin networkinterface.Plugin,
	opts NetworkInterfaceReconcilerOptions,
) (*NetworkInterfaceReconciler, error) {

	return &NetworkInterfaceReconciler{
		log: log,
		queue: workqueue.NewTypedRateLimitingQueue[string](
			workqueue.DefaultTypedControllerRateLimiter[string](),
		),
		EventRecorder:          eventRecorder,
		paths:                  opts.Paths,
		networkInterfacePlugin: nicPlugin,
		nics:                   nics,
		nicEvents:              nicEvents,
	}, nil
}

type NetworkInterfaceReconciler struct {
	log   logr.Logger
	queue workqueue.TypedRateLimitingInterface[string]

	paths host.Paths

	networkInterfacePlugin networkinterface.Plugin

	nics      store.Store[*api.NetworkInterface]
	nicEvents event.Source[*api.NetworkInterface]

	recorder.EventRecorder
}

func (r *NetworkInterfaceReconciler) Start(ctx context.Context) error {
	log := r.log

	// TODO make configurable
	workerSize := 15

	eventHandlerRegistration, err := r.nicEvents.AddHandler(
		event.HandlerFunc[*api.NetworkInterface](func(evt event.Event[*api.NetworkInterface]) {
			r.queue.Add(evt.Object.ID)
		}))
	if err != nil {
		return err
	}
	defer func() {
		if err = r.nicEvents.RemoveHandler(eventHandlerRegistration); err != nil {
			log.Error(err, "failed to remove nic event handler")
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

func (r *NetworkInterfaceReconciler) processNextWorkItem(ctx context.Context, log logr.Logger) bool {
	id, shutdown := r.queue.Get()
	if shutdown {
		return false
	}
	defer r.queue.Done(id)

	log = log.WithValues("nicID", id)
	ctx = logr.NewContext(ctx, log)

	if err := r.reconcileNetworkInterface(ctx, id); err != nil {
		log.Error(err, "failed to reconcile machine")
		r.queue.AddRateLimited(id)
		return true
	}

	r.queue.Forget(id)
	return true
}

func (r *NetworkInterfaceReconciler) reconcileNetworkInterface(ctx context.Context, id string) error {
	log := logr.FromContextOrDiscard(ctx)

	log.V(2).Info("Getting machine from store", "id", id)
	nic, err := r.nics.Get(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("failed to fetch nic from store: %w", err)
		}

		return nil
	}

	if nic.DeletedAt != nil {
		if len(nic.Finalizers) > 1 {
			log.V(1).Info("Finalizers from dependencies still present")
			return nil
		}

		if err := r.deleteNic(ctx, log, nic); err != nil {
			return fmt.Errorf("failed to delete nic: %w", err)
		}
		log.V(1).Info("Successfully deleted nic")
		return nil
	}

	if !slices.Contains(nic.Finalizers, NetworkInterfaceFinalizer) {
		nic.Finalizers = append(nic.Finalizers, NetworkInterfaceFinalizer)
		if _, err := r.nics.Update(ctx, nic); err != nil {
			return fmt.Errorf("failed to set finalizers: %w", err)
		}
		return nil
	}

	machineName := getMachineNameFromNicID(id)
	if machineName == nil {
		return fmt.Errorf("failed to get machine name: invalid nic id")
	}

	nicState, err := r.networkInterfacePlugin.Apply(ctx, &nic.Spec, ptr.Deref(machineName, ""))
	if err != nil {
		return fmt.Errorf("failed to apply network interface: %w", err)
	}

	nic.Status = ptr.Deref(nicState, api.NetworkInterfaceStatus{
		State: api.NetworkInterfaceStatePending,
	})

	if _, err := r.nics.Update(ctx, nic); err != nil {
		return fmt.Errorf("failed to update network interface: %w", err)
	}

	return nil
}

func (r *NetworkInterfaceReconciler) deleteNic(ctx context.Context, log logr.Logger, nic *api.NetworkInterface) error {
	if !slices.Contains(nic.Finalizers, NetworkInterfaceFinalizer) {
		log.V(1).Info("image has no finalizer: done")
		return nil
	}

	machineName := getMachineNameFromNicID(nic.ID)
	if machineName == nil {
		return fmt.Errorf("failed to get machine name: invalid nic id")
	}

	if err := r.networkInterfacePlugin.Delete(ctx, nic.Spec.Name, ptr.Deref(machineName, "")); err != nil {
		return fmt.Errorf("failed to apply network interface: %w", err)
	}
	log.V(2).Info("Rbd image deleted")

	nic.Finalizers = utils.DeleteSliceElement(nic.Finalizers, NetworkInterfaceFinalizer)
	if _, err := r.nics.Update(ctx, nic); store.IgnoreErrNotFound(err) != nil {
		return fmt.Errorf("failed to update image metadata: %w", err)
	}
	log.V(2).Info("Removed Finalizers")

	return nil
}
