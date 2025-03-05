// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"

	iri "github.com/ironcore-dev/ironcore/iri/apis/machine/v1alpha1"
)

func (s *Server) ListEvents(ctx context.Context, req *iri.ListEventsRequest) (*iri.ListEventsResponse, error) {
	_ = s.loggerFrom(ctx)
	return &iri.ListEventsResponse{}, nil
}
