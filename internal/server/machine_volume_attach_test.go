// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"fmt"

	iri "github.com/ironcore-dev/ironcore/iri/apis/machine/v1alpha1"
	irimeta "github.com/ironcore-dev/ironcore/iri/apis/meta/v1alpha1"
	machinepoolletv1alpha1 "github.com/ironcore-dev/ironcore/poollet/machinepoollet/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AttachVolume", func() {
	It("should correctly attach volume to machine", func(ctx SpecContext) {
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
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(createResp).NotTo(BeNil())

		volume := &iri.Volume{
			Name: "disk-1",
			EmptyDisk: &iri.EmptyDisk{
				SizeBytes: emptyDiskSize,
			},
			Device: "oda",
		}

		By("attaching a volume")
		machineID := createResp.Machine.Metadata.Id
		Expect(machineClient.AttachVolume(ctx, &iri.AttachVolumeRequest{
			MachineId: machineID,
			Volume:    volume,
		})).Error().NotTo(HaveOccurred())

		updatedMachine, err := machineClient.ListMachines(ctx, &iri.ListMachinesRequest{
			Filter: &iri.MachineFilter{
				Id: machineID,
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedMachine.Machines).To(HaveLen(1))
		Expect(updatedMachine.Machines[0].Spec.Volumes).To(HaveLen(1))
		Expect(updatedMachine.Machines[0].Spec.Volumes).To(ContainElement(
			WithTransform(func(v *iri.Volume) string {
				return fmt.Sprintf("%s-%s-%d", v.Name, v.Device, v.EmptyDisk.SizeBytes)
			}, Equal(fmt.Sprintf("%s-%s-%d", volume.Name, volume.Device, volume.EmptyDisk.SizeBytes))),
		))
	})
})
