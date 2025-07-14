// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	iri "github.com/ironcore-dev/ironcore/iri/apis/machine/v1alpha1"
	irimeta "github.com/ironcore-dev/ironcore/iri/apis/meta/v1alpha1"
	machinepoolletv1alpha1 "github.com/ironcore-dev/ironcore/poollet/machinepoollet/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("DetachNetworkInterface", func() {
	It("should detach a network interface from the machine", func(ctx SpecContext) {
		By("creating a machine")
		createResp, err := machineClient.CreateMachine(ctx, &iri.CreateMachineRequest{
			Machine: &iri.Machine{
				Metadata: &irimeta.ObjectMetadata{
					Labels: map[string]string{
						machinepoolletv1alpha1.MachineUIDLabel: "foobar",
					},
				},
				Spec: &iri.MachineSpec{
					Power: iri.Power_POWER_ON,
					Class: machineClassName,
					NetworkInterfaces: []*iri.NetworkInterface{
						{
							Name:      "nic-1",
							NetworkId: "nid-1",
							Ips:       []string{"192.168.1.1"},
							Attributes: map[string]string{
								"key1": "value1",
							},
						},
					},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(createResp).NotTo(BeNil())

		By("detaching the network interface")
		nicName := "nic-1"
		machineID := createResp.Machine.Metadata.Id
		Expect(machineClient.DetachNetworkInterface(ctx, &iri.DetachNetworkInterfaceRequest{
			MachineId: machineID,
			Name:      nicName,
		})).Error().NotTo(HaveOccurred())

		machine, err := machineStore.Get(ctx, machineID)
		Expect(err).NotTo(HaveOccurred())
		Expect(machine.Spec.NetworkInterfaces).To(ContainElement(Satisfy(func(v *api.NetworkInterfaceSpec) bool {
			return v.DeletedAt != nil && v.Name == nicName
		})))
	})
})
