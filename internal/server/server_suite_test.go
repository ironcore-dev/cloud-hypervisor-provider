// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/cmd/cloud-hypervisor-provider/app"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/mcr"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/server"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/strategy"
	iriv1alpha1 "github.com/ironcore-dev/ironcore/iri/apis/machine/v1alpha1"
	"github.com/ironcore-dev/ironcore/iri/remote/machine"
	"github.com/ironcore-dev/provider-utils/eventutils/event"
	hostutils "github.com/ironcore-dev/provider-utils/storeutils/host"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const (
	eventuallyTimeout    = 80 * time.Second
	pollingInterval      = 50 * time.Millisecond
	consistentlyDuration = 1 * time.Second

	machineClass  = "sample-machine-class"
	emptyDiskSize = 1024 * 1024 * 1024
)

var (
	machineClient iriv1alpha1.MachineRuntimeClient
	machineEvents *event.ListWatchSource[*api.Machine]

	tempDir string
)

func TestServer(t *testing.T) {
	SetDefaultConsistentlyPollingInterval(pollingInterval)
	SetDefaultEventuallyPollingInterval(pollingInterval)
	SetDefaultEventuallyTimeout(eventuallyTimeout)
	SetDefaultConsistentlyDuration(consistentlyDuration)

	RegisterFailHandler(Fail)
	RunSpecs(t, "GRPC Server Suite", Label("integration"))
}

var _ = BeforeEach(func() {
	log := zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true))
	logf.SetLogger(log)

	tempDir = GinkgoT().TempDir()
	Expect(os.Chmod(tempDir, 0730)).Should(Succeed())

	By("preparing the host dirs")
	providerHost, err := host.PathsAt(tempDir)
	Expect(err).NotTo(HaveOccurred())

	By("setting up the machine store")
	machineStore, err := hostutils.NewStore[*api.Machine](hostutils.Options[*api.Machine]{
		Dir:            providerHost.MachineStoreDir(),
		NewFunc:        func() *api.Machine { return &api.Machine{} },
		CreateStrategy: strategy.MachineStrategy,
	})
	Expect(err).NotTo(HaveOccurred())

	By("setting up the machine events")
	machineEvents, err = event.NewListWatchSource[*api.Machine](
		machineStore.List,
		machineStore.Watch,
		event.ListWatchSourceOptions{},
	)
	Expect(err).NotTo(HaveOccurred())

	classRegistry, err := mcr.NewMachineClassRegistry([]mcr.MachineClass{
		{
			Name:        "experimental",
			CpuMillis:   1000,
			MemoryBytes: 2147483648,
		},
	})
	Expect(err).NotTo(HaveOccurred())

	srv, err := server.New(machineStore, server.Options{
		MachineClassRegistry: classRegistry,
	})
	Expect(err).NotTo(HaveOccurred())

	cancelCtx, cancel := context.WithCancel(context.Background())
	DeferCleanup(cancel)

	go func() {
		defer GinkgoRecover()
		Expect(app.RunGRPCServer(cancelCtx, log, log, srv, filepath.Join(tempDir, "test.sock"))).To(Succeed())
	}()

	go func() {
		defer GinkgoRecover()
		Expect(machineEvents.Start(cancelCtx)).To(Succeed())
	}()

	Eventually(func() error {
		return isSocketAvailable(filepath.Join(tempDir, "test.sock"))
	}).
		WithTimeout(30 * time.Second).
		WithPolling(500 * time.Millisecond).
		Should(Succeed())

	address, err := machine.GetAddressWithTimeout(
		3*time.Second, fmt.Sprintf("unix://%s",
			filepath.Join(tempDir, "test.sock")),
	)
	Expect(err).NotTo(HaveOccurred())

	gconn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(gconn.Close)

	machineClient = iriv1alpha1.NewMachineRuntimeClient(gconn)
})

func isSocketAvailable(socketPath string) error {
	fileInfo, err := os.Stat(socketPath)
	if err != nil {
		return err
	}
	if fileInfo.Mode()&os.ModeSocket != 0 {
		return nil
	}
	return fmt.Errorf("socket %s is not available", socketPath)
}
