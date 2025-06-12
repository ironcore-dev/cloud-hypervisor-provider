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

var _ = Describe("CreateMachine", func() {
	It("should create simple machine ", func(ctx SpecContext) {
		By("creating a machine with power on and machine class")
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

		By("ensuring the correct creation response")
		Expect(createResp).Should(SatisfyAll(
			HaveField("Machine.Metadata.Id", Not(BeEmpty())),
			HaveField("Machine.Spec.Power", iri.Power_POWER_ON),
			HaveField("Machine.Spec.Image", BeNil()),
			HaveField("Machine.Spec.Class", machineClassName),
			HaveField("Machine.Spec.IgnitionData", BeNil()),
			HaveField("Machine.Spec.Volumes", BeNil()),
			HaveField("Machine.Spec.NetworkInterfaces", BeNil()),
			HaveField("Machine.Status.ObservedGeneration", BeZero()),
			HaveField("Machine.Status.State", Equal(iri.MachineState_MACHINE_PENDING)),
			HaveField("Machine.Status.ImageRef", BeEmpty()),
			HaveField("Machine.Status.Volumes", BeNil()),
			HaveField("Machine.Status.NetworkInterfaces", BeNil()),
		))
	})

})
