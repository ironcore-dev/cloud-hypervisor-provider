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
					Class: machineClassName,
					Volumes: []*iri.Volume{
						{
							Name: "disk-1",
							LocalDisk: &iri.LocalDisk{
								SizeBytes: emptyDiskSize,
							},
							Device: "oda",
						},
						{
							Name: "disk-2",
							LocalDisk: &iri.LocalDisk{
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

		By("detaching a volume")
		diskName := "disk-2"
		machineID := createResp.Machine.Metadata.Id
		Expect(machineClient.DetachVolume(ctx, &iri.DetachVolumeRequest{
			MachineId: machineID,
			Name:      diskName,
		})).Error().NotTo(HaveOccurred())

		machine, err := machineStore.Get(ctx, machineID)
		Expect(err).NotTo(HaveOccurred())
		Expect(machine.Spec.Volumes).To(ContainElement(Satisfy(func(v *api.VolumeSpec) bool {
			return v.DeletedAt != nil && v.Name == diskName
		})))
	})
})
