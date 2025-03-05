// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"

	iri "github.com/ironcore-dev/ironcore/iri/apis/machine/v1alpha1"
)

func (s *Server) CreateMachine(ctx context.Context, req *iri.CreateMachineRequest) (res *iri.CreateMachineResponse, retErr error) {
	_ = s.loggerFrom(ctx)
	return &iri.CreateMachineResponse{}, nil
}
