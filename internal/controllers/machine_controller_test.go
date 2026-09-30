// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
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

			By("waiting for the cloud-hypervisor api socket to become ready")
			sockPath := hostPaths.MachineChSocket(machineID)
			Eventually(func(g Gomega) error {
				c, err := vmm.NewUnixSocketClient(sockPath)
				g.Expect(err).NotTo(HaveOccurred())
				_, err = c.GetVmmPingWithResponse(ctx)
				return err
			}).Should(Succeed())

			chClient, err := vmm.NewUnixSocketClient(sockPath)
			Expect(err).NotTo(HaveOccurred())

			By("checking that the vmm is ok")
			resp, err := chClient.GetVmmPingWithResponse(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode()).To(Equal(http.StatusOK))

			Eventually(func(g Gomega) client.VmState {
				resp, err := chClient.GetVmInfoWithResponse(ctx)
				g.Expect(err).NotTo(HaveOccurred())

				g.Expect(resp).NotTo(BeNil())
				g.Expect(resp.JSON200).NotTo(BeNil())

				return resp.JSON200.State
			}).Should(Equal(client.Running))

			Expect(machineStore.Delete(ctx, machineID)).Should(Succeed())

			By("waiting for the machine to be deleted")
			Eventually(func(g Gomega) *time.Time {
				machine, err := machineStore.Get(ctx, machineID)
				g.Expect(err).NotTo(HaveOccurred())

				return machine.DeletedAt
			}).ShouldNot(BeNil())

			By("verifying the cloud-hypervisor process is gone")
			// After deletion the process is terminated, so the VM is either
			// reported as not created or the socket no longer answers.
			Eventually(func(g Gomega) {
				resp, err := chClient.GetVmInfoWithResponse(ctx)
				if err != nil {
					return
				}
				g.Expect(string(resp.Body)).To(ContainSubstring("VM is not created"))
			}).Should(Succeed())
		})
	})
})
