// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package options

import (
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/networkinterface"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/networkinterface/ini"
	"github.com/spf13/pflag"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

type iniOptions struct{ PluginPath string }

func (o *iniOptions) PluginName() string { return ini.PluginName }

func (o *iniOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.PluginPath, "ini-plugin-path", "", "Path to the INI network interface plugin executable")
}

func (o *iniOptions) NetworkInterfacePlugin() (networkinterface.Plugin, func(), error) {
	plugin, err := ini.NewPlugin(o.PluginPath)
	if err != nil {
		return nil, nil, err
	}
	return plugin, nil, nil
}

func init() {
	utilruntime.Must(DefaultPluginTypeRegistry.Register(&iniOptions{}, 3))
}
