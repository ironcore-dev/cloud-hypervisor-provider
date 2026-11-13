// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package options

import (
	"fmt"
	"net/netip"

	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	providerplugin "github.com/ironcore-dev/provider-utils/plugin"
	"github.com/spf13/pflag"
	//TODO update once published
	"go.moehl.dev/ironcore/inisix"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

type inisixOptions struct {
	PrefixStore string
	DNSServers  []string
}

func (o *inisixOptions) PluginName() string {
	return "inisix"
}

func (o *inisixOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.PrefixStore, "inisix-prefix-store", "/var/lib/inisix/prefixes.json", "Path to the inisix IPv6 prefix store")
	fs.StringSliceVar(&o.DNSServers, "inisix-dns-server", nil, "IPv6 DNS server announced to guests; repeatable")
}

func (o *inisixOptions) NetworkInterfacePlugin(_ host.Paths) (providerplugin.NetworkInterface, error) {
	dnsServers := make([]netip.Addr, 0, len(o.DNSServers))
	for _, value := range o.DNSServers {
		addr, err := netip.ParseAddr(value)
		if err != nil || !addr.Is6() || addr.Is4In6() || addr.Zone() != "" {
			return nil, fmt.Errorf("invalid inisix DNS server %q", value)
		}
		dnsServers = append(dnsServers, addr.Unmap())
	}

	plugin, err := inisix.New(o.PrefixStore, dnsServers)
	if err != nil {
		return nil, err
	}
	return plugin, nil
}

func init() {
	utilruntime.Must(DefaultPluginTypeRegistry.Register(&inisixOptions{}, 2))
}
