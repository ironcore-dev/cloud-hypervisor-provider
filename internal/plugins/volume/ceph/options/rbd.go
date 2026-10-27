// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package options

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/volume"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/volume/ceph"
	"github.com/spf13/pflag"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

type rbdOptions struct{}

func (o *rbdOptions) PluginName() string {
	return "rbd"
}

func (o *rbdOptions) AddFlags(_ *pflag.FlagSet) {}

func (o *rbdOptions) VolumePlugin(_ context.Context, log logr.Logger, paths host.Paths) (volume.Plugin, error) {
	return ceph.NewRBDPlugin(log, paths), nil
}

func init() {
	utilruntime.Must(DefaultPluginTypeRegistry.Register(&rbdOptions{}, 5))
}
