// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package isolated

import (
	"context"
	"os"

	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/networkinterface"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	pluginIsolated = "isolated"
)

type plugin struct {
	host host.Paths
}

func NewPlugin() networkinterface.Plugin {
	return &plugin{}
}

func (p *plugin) Init(host host.Paths) error {
	p.host = host
	return nil
}

func (p *plugin) Apply(ctx context.Context,
	spec *api.NetworkInterfaceSpec,
	machineID string,
) (*api.NetworkInterfaceStatus, error) {
	log := ctrl.LoggerFrom(ctx)

	log.V(1).Info("Writing network interface dir")
	if err := os.MkdirAll(p.host.MachineNetworkInterfaceDir(machineID, spec.Name), os.ModePerm); err != nil {
		return nil, err
	}

	return &api.NetworkInterfaceStatus{
		State: api.NetworkInterfaceStatePending,
	}, nil
}

func (p *plugin) Delete(ctx context.Context, computeNicName string, machineID string) error {
	return os.RemoveAll(p.host.MachineNetworkInterfaceDir(machineID, computeNicName))
}

func (p *plugin) Name() string {
	return pluginIsolated
}
