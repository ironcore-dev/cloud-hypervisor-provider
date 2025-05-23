// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"

	iri "github.com/ironcore-dev/ironcore/iri/apis/machine/v1alpha1"
)

type MachineClass struct {
	Name        string
	CpuMillis   int64
	MemoryBytes int64
}

func (s *Server) Status(ctx context.Context, req *iri.StatusRequest) (*iri.StatusResponse, error) {
	log := s.loggerFrom(ctx)

	var classes []*iri.MachineClassStatus
	for _, class := range s.supportedMachineClasses {
		classes = append(classes, &iri.MachineClassStatus{
			MachineClass: &iri.MachineClass{
				Name: class.Name,
				Capabilities: &iri.MachineClassCapabilities{
					CpuMillis:   class.CpuMillis,
					MemoryBytes: class.MemoryBytes,
				},
			},
			//TODO will be deprecated soon
			Quantity: 1000,
		})
	}

	log.V(1).Info("Returning machine classes")
	return &iri.StatusResponse{
		MachineClassStatus: classes,
	}, nil
}
