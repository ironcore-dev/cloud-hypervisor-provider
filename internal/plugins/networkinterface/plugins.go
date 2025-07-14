// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package networkinterface

import (
	"context"

	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
)

type Plugin interface {
	Name() string
	Init(host host.Paths) error

	Apply(ctx context.Context, spec *api.NetworkInterfaceSpec, machineID string) (*api.NetworkInterfaceStatus, error)
	Delete(ctx context.Context, computeNicName string, machineID string) error
}
