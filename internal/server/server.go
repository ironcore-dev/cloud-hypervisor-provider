// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/machineclasses"
	"github.com/ironcore-dev/ironcore/broker/common/idgen"
	iri "github.com/ironcore-dev/ironcore/iri/apis/machine/v1alpha1"
	"github.com/ironcore-dev/provider-utils/claimutils/claim"
	"github.com/ironcore-dev/provider-utils/eventutils/recorder"
	"github.com/ironcore-dev/provider-utils/storeutils/store"
	"github.com/ironcore-dev/provider-utils/storeutils/utils"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ iri.MachineRuntimeServer = (*Server)(nil)

type Server struct {
	idGen idgen.IDGen

	iri.UnimplementedMachineRuntimeServer

	machineClassRegistry machineclasses.Registry

	machineStore store.Store[*api.Machine]
	eventStore   recorder.EventStore

	resourceClaimer claim.Claimer
}

type Options struct {
	IDGen idgen.IDGen

	EventStore recorder.EventStore

	MachineClassRegistry machineclasses.Registry

	ResourceClaimer claim.Claimer
}

type nilEventStore struct{}

func (n *nilEventStore) ListEvents() []*recorder.Event {
	return nil
}

func setOptionsDefaults(o *Options) {
	if o.IDGen == nil {
		o.IDGen = utils.IdGenerateFunc(uuid.NewString)
	}
	if o.EventStore == nil {
		o.EventStore = &nilEventStore{}
	}
}

func New(store store.Store[*api.Machine], opts Options) (*Server, error) {
	setOptionsDefaults(&opts)

	if opts.MachineClassRegistry == nil {
		return nil, fmt.Errorf("MachineClassRegistry option is required")
	}

	return &Server{
		idGen:                opts.IDGen,
		machineStore:         store,
		eventStore:           opts.EventStore,
		machineClassRegistry: opts.MachineClassRegistry,
		resourceClaimer:      opts.ResourceClaimer,
	}, nil
}

// nolint:unparam
func (s *Server) loggerFrom(ctx context.Context, keysWithValues ...interface{}) logr.Logger {
	return ctrl.LoggerFrom(ctx, keysWithValues...)
}
