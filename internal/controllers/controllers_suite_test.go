// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"os"
	"path"
	"testing"
	"time"

	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/controllers"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/networkinterface/isolated"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/volume"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/volume/localdisk"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/raw"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/strategy"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/vmm"
	"github.com/ironcore-dev/ironcore-image/oci/remote"
	ocistore "github.com/ironcore-dev/ironcore-image/oci/store"
	"github.com/ironcore-dev/provider-utils/eventutils/event"
	"github.com/ironcore-dev/provider-utils/eventutils/recorder"
	ocihostutils "github.com/ironcore-dev/provider-utils/ociutils/host"
	ociutils "github.com/ironcore-dev/provider-utils/ociutils/oci"
	hostutils "github.com/ironcore-dev/provider-utils/storeutils/host"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const (
	eventuallyTimeout    = 180 * time.Second
	pollingInterval      = 50 * time.Millisecond
	consistentlyDuration = 1 * time.Second
	osImage              = "ghcr.io/ironcore-dev/os-images/virtualization/gardenlinux:latest"
)

var (
	machineStore  *hostutils.Store[*api.Machine]
	eventRecorder *recorder.Store
)

func TestControllers(t *testing.T) {
	SetDefaultConsistentlyPollingInterval(pollingInterval)
	SetDefaultEventuallyPollingInterval(pollingInterval)
	SetDefaultEventuallyTimeout(eventuallyTimeout)
	SetDefaultConsistentlyDuration(consistentlyDuration)

	RegisterFailHandler(Fail)
	RunSpecs(t, "Machine Controller Suite", Label("integration"))
}

var _ = BeforeSuite(func(ctx context.Context) {
	log := zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true))
	logf.SetLogger(log)

	By("setting up test environment")
	rootDir, err := os.MkdirTemp("", "chp-test-*")
	Expect(err).NotTo(HaveOccurred())
	Expect(os.Chmod(rootDir, 0755)).To(Succeed())
	DeferCleanup(func() { os.RemoveAll(rootDir) })

	hostPaths, err := host.PathsAt(rootDir)
	Expect(err).NotTo(HaveOccurred())

	platform, err := ocihostutils.Platform()
	Expect(err).NotTo(HaveOccurred())

	reg, err := remote.DockerRegistryWithPlatform(nil, platform)
	Expect(err).NotTo(HaveOccurred())

	ociStore, err := ocistore.New(hostPaths.ImagesDir())
	Expect(err).NotTo(HaveOccurred())

	rawInst, err := raw.Instance(raw.Default())
	Expect(err).NotTo(HaveOccurred())

	imgCache, err := ociutils.NewLocalCache(log, reg, ociStore, nil)
	Expect(err).NotTo(HaveOccurred())

	volumePlugins := volume.NewPluginManager()
	Expect(volumePlugins.InitPlugins(hostPaths, []volume.Plugin{
		localdisk.NewPlugin(rawInst, imgCache),
	})).NotTo(HaveOccurred())

	nicPlugin := isolated.NewPlugin()
	Expect(nicPlugin.Init(hostPaths)).NotTo(HaveOccurred())

	machineStore, err = hostutils.NewStore[*api.Machine](hostutils.Options[*api.Machine]{
		Dir:            path.Join(rootDir, "store"),
		NewFunc:        func() *api.Machine { return &api.Machine{} },
		CreateStrategy: strategy.MachineStrategy,
	})
	Expect(err).NotTo(HaveOccurred())

	machineEvents, err := event.NewListWatchSource[*api.Machine](
		machineStore.List,
		machineStore.Watch,
		event.ListWatchSourceOptions{},
	)
	Expect(err).NotTo(HaveOccurred())

	chSocketDir := os.Getenv("CH_SOCKET_DIR")
	if chSocketDir == "" {
		log.V(1).Info("use default socket directory")
		chSocketDir = "/run/chp/ch"
	}

	chFirmwarePath := os.Getenv("CH_FIRMWARE_PATH")
	if chFirmwarePath == "" {
		log.V(1).Info("use default firmware path")
		chFirmwarePath = "/usr/local/bin/hypervisor-fw"
	}

	virtualMachineManager, err := vmm.NewManager(
		log.WithName("virtual-machine-manager"),
		hostPaths,
		vmm.ManagerOptions{
			CHSocketsPath:     chSocketDir,
			FirmwarePath:      chFirmwarePath,
			ReservedInstances: nil,
		},
	)
	Expect(err).NotTo(HaveOccurred())

	eventRecorder = recorder.NewEventStore(log, recorder.EventStoreOptions{})
	machineReconciler, err := controllers.NewMachineReconciler(
		log.WithName("machine-reconciler"),
		machineStore,
		machineEvents,
		eventRecorder,
		virtualMachineManager,
		volumePlugins,
		nicPlugin,
		controllers.MachineReconcilerOptions{
			ImageCache: imgCache,
			Raw:        rawInst,
			Paths:      hostPaths,
		},
	)
	Expect(err).NotTo(HaveOccurred())

	cancelCtx, cancel := context.WithCancel(context.Background())
	DeferCleanup(cancel)

	go func() {
		defer GinkgoRecover()
		Expect(imgCache.Start(cancelCtx)).To(Succeed())
	}()

	go func() {
		defer GinkgoRecover()
		Expect(machineReconciler.Start(cancelCtx)).To(Succeed())
	}()

	go func() {
		defer GinkgoRecover()
		Expect(machineEvents.Start(cancelCtx)).To(Succeed())
	}()

	go func() {
		defer GinkgoRecover()
		eventRecorder.Start(cancelCtx)
	}()

	// Wait for services to start
	time.Sleep(200 * time.Millisecond)

})
