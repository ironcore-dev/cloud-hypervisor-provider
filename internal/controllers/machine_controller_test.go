// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"net/http"

	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/vmm"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/ptr"
)

var _ = Describe("MachineController", func() {
	Context("Machine Lifecycle", func(ctx context.Context) {
		var machineID string

		It("should create and reconcile a machine", func(ctx SpecContext) {
			By("creating a machine in the store")
			machine, err := machineStore.Create(ctx, &api.Machine{
				Spec: api.MachineSpec{
					Power:       api.PowerStatePowerOn,
					Cpu:         4,
					MemoryBytes: 4294967296, // 4GB
					Image:       ptr.To(osImage),
					//Volumes:           []*api.VolumeSpec{
					//	{
					//		Name:       "root",
					//		Device:     "oda",
					//		EmptyDisk:  &api.EmptyDiskSpec{
					//		},
					//	},
					//},
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(machine).NotTo(BeNil())
			Expect(machine.ID).NotTo(BeEmpty())

			GinkgoWriter.Printf("Created machine: ID=%s\n", machineID)

			Eventually(machine.Spec.ApiSocketPath).ShouldNot(BeEmpty())

			chClient, err := vmm.NewUnixSocketClient(ptr.Deref(machine.Spec.ApiSocketPath, ""))
			Expect(err).NotTo(HaveOccurred())

			resp, err := chClient.GetVmmPingWithResponse(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode()).To(Equal(http.StatusOK))
		})

	})
})
