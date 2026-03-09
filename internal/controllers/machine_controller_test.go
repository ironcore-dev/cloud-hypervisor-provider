// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/cloud-hypervisor/client"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/vmm"
	apiutils "github.com/ironcore-dev/provider-utils/apiutils/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/ptr"
)

var _ = Describe("MachineController", func() {
	Context("Machine Lifecycle", func() {
		machineID := uuid.NewString()

		It("should create and reconcile a machine", func(ctx SpecContext) {
			By("creating a machine in the store")
			machine, err := machineStore.Create(ctx, &api.Machine{
				Metadata: apiutils.Metadata{
					ID: machineID,
				},
				Spec: api.MachineSpec{
					Power:       api.PowerStatePowerOn,
					Cpu:         2,
					MemoryBytes: 2147483648,
					Volumes: []*api.VolumeSpec{
						{
							Name:   "root",
							Device: "oda",
							LocalDisk: &api.LocalDiskSpec{
								Image: ptr.To(osImage),
							},
						},
					},
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(machine).NotTo(BeNil())
			Expect(machine.ID).NotTo(BeEmpty())

			GinkgoWriter.Printf("Created machine: ID=%s\n", machineID)

			By("verifying image pulling event was recorded")
			Eventually(func(g Gomega) bool {
				events := eventRecorder.ListEvents()
				GinkgoWriter.Printf("Total events recorded: %d\n", len(events))

				for _, evt := range events {
					if evt.InvolvedObjectMeta.ID == machineID && evt.Reason == "PullingImage" {
						GinkgoWriter.Printf("Found PullingImage event for machine %s: %s\n", machineID, evt.Message)
						return true
					}
				}

				return false
			}).Should(BeTrue())

			By("waiting for the api socket path to be set")
			Eventually(func(g Gomega) *string {
				machine, err := machineStore.Get(ctx, machineID)
				g.Expect(err).NotTo(HaveOccurred())

				return machine.Spec.ApiSocketPath
			}).ShouldNot(BeNil())

			machine, err = machineStore.Get(ctx, machineID)
			Expect(err).NotTo(HaveOccurred())

			chClient, err := vmm.NewUnixSocketClient(ptr.Deref(machine.Spec.ApiSocketPath, ""))
			Expect(err).NotTo(HaveOccurred())

			By("checking that the vmm is ok")
			resp, err := chClient.GetVmmPingWithResponse(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode()).To(Equal(http.StatusOK))

			Eventually(func(g Gomega) client.VmInfoState {
				resp, err := chClient.GetVmInfoWithResponse(ctx)
				g.Expect(err).NotTo(HaveOccurred())

				g.Expect(resp).NotTo(BeNil())
				g.Expect(resp.JSON200).NotTo(BeNil())

				return resp.JSON200.State
			}).Should(Equal(client.Running))

			Expect(machineStore.Delete(ctx, machineID)).Should(Succeed())

			By("waiting for the api socket path to be set")
			Eventually(func(g Gomega) *time.Time {
				machine, err := machineStore.Get(ctx, machineID)
				g.Expect(err).NotTo(HaveOccurred())

				return machine.DeletedAt
			}).ShouldNot(BeNil())

			Eventually(func(g Gomega) string {
				resp, err := chClient.GetVmInfoWithResponse(ctx)
				g.Expect(err).NotTo(HaveOccurred())

				return string(resp.Body)
			}).Should(ContainSubstring("VM is not created"))
		})
	})
})
