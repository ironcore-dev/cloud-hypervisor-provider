// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package options

import (
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/networkinterface/inisix"
	providerplugin "github.com/ironcore-dev/provider-utils/plugin"
	"github.com/spf13/pflag"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

type inisixOptions struct {
	PluginPath  string
	Mode        string
	PrefixStore string
	VFStore     string
}

func (o *inisixOptions) PluginName() string {
	return inisix.PluginName
}

func (o *inisixOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.PluginPath, "inisix-plugin-path", "/usr/local/bin/inisix", "Path to the inisix plugin executable")
	fs.StringVar(&o.Mode, "inisix-mode", "tap", "inisix mode (tap or vf)")
	fs.StringVar(&o.PrefixStore, "inisix-prefix-store", "", "Path to the inisix IPv6 prefix store")
	fs.StringVar(&o.VFStore, "inisix-vf-store", "", "Path to the inisix virtual-function store")
}

func (o *inisixOptions) NetworkInterfacePlugin(paths host.Paths) (providerplugin.NetworkInterface, func(), error) {
	plugin, err := inisix.NewPlugin(paths, inisix.Config{
		PluginPath:  o.PluginPath,
		Mode:        o.Mode,
		PrefixStore: o.PrefixStore,
		VFStore:     o.VFStore,
	})
	if err != nil {
		return nil, nil, err
	}
	return plugin, nil, nil
}

func init() {
	utilruntime.Must(DefaultPluginTypeRegistry.Register(&inisixOptions{}, 2))
}
