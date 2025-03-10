// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	iri "github.com/ironcore-dev/ironcore/iri/apis/machine/v1alpha1"
	irimeta "github.com/ironcore-dev/ironcore/iri/apis/meta/v1alpha1"
	machinepoolletv1alpha1 "github.com/ironcore-dev/ironcore/poollet/machinepoollet/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("DetachVolume", func() {
	It("should correctly detach volume from machine", func(ctx SpecContext) {
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
					Class: machineClass,
					Volumes: []*iri.Volume{
						{
							Name: "disk-1",
							EmptyDisk: &iri.EmptyDisk{
								SizeBytes: emptyDiskSize,
							},
							Device: "oda",
						},
						{
							Name: "disk-2",
							EmptyDisk: &iri.EmptyDisk{
								SizeBytes: emptyDiskSize,
							},
							Device: "odb",
						},
					},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(createResp).NotTo(BeNil())

		By("attaching a volume")
		machineID := createResp.Machine.Metadata.Id
		Expect(machineClient.DetachVolume(ctx, &iri.DetachVolumeRequest{
			MachineId: machineID,
			Name:      "disk-2",
		})).Error().NotTo(HaveOccurred())

		updatedMachine, err := machineClient.ListMachines(ctx, &iri.ListMachinesRequest{
			Filter: &iri.MachineFilter{
				Id: machineID,
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedMachine.Machines).To(HaveLen(1))
		Expect(updatedMachine.Machines[0].Spec.Volumes).To(HaveLen(1))
		Expect(updatedMachine.Machines[0].Spec.Volumes[0].Name).To(Equal("disk-1"))

	})
})
