// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package apinet

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/networkinterface"
	apinetv1alpha1 "github.com/ironcore-dev/ironcore-net/api/core/v1alpha1"
	apinet "github.com/ironcore-dev/ironcore-net/apimachinery/api/net"
	"github.com/ironcore-dev/ironcore-net/apinetlet/provider"
	apinetv1alpha1apply "github.com/ironcore-dev/ironcore-net/client-go/applyconfigurations/core/v1alpha1"
	plugin "github.com/ironcore-dev/provider-utils/plugin"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	fieldOwner = client.FieldOwner("networking.ironcore.dev/cloud-hypervisor-provider")

	defaultAPINetConfigFile = "api-net.json"

	filePerm     = 0666
	pluginAPInet = "apinet"
)

type Plugin struct {
	nodeName     string
	host         host.Paths
	apinetClient client.Client
}

func NewPlugin(nodeName string, client client.Client, paths host.Paths) networkinterface.Plugin {
	return &Plugin{
		nodeName:     nodeName,
		host:         paths,
		apinetClient: client,
	}
}

func ironcoreIPsToAPInetIPs(ips []string) []apinet.IP {
	res := make([]apinet.IP, len(ips))
	for i, ip := range ips {
		res[i] = apinet.MustParseIP(ip)
	}
	return res
}

type apiNetNetworkInterfaceConfig struct {
	Namespace string `json:"namespace"`
}

func (p *Plugin) apiNetNetworkInterfaceConfigFile(machineID string, networkInterfaceName string) string {
	return filepath.Join(p.host.MachineNetworkInterfaceDir(machineID, networkInterfaceName), defaultAPINetConfigFile)
}

func (p *Plugin) writeAPINetNetworkInterfaceConfig(
	machineID string, networkInterfaceName string,
	cfg *apiNetNetworkInterfaceConfig,
) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(p.apiNetNetworkInterfaceConfigFile(machineID, networkInterfaceName), data, filePerm)
}

func (p *Plugin) readAPINetNetworkInterfaceConfig(
	machineID string,
	networkInterfaceName string,
) (*apiNetNetworkInterfaceConfig, error) {
	data, err := os.ReadFile(p.apiNetNetworkInterfaceConfigFile(machineID, networkInterfaceName))
	if err != nil {
		return nil, err
	}

	cfg := &apiNetNetworkInterfaceConfig{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (p *Plugin) APInetNicName(machineID string, networkInterfaceName string) string {
	return uuid.NewHash(sha256.New(), uuid.Nil, []byte(fmt.Sprintf("%s/%s", machineID, networkInterfaceName)), 5).String()
}

func (p *Plugin) Apply(
	ctx context.Context,
	spec *plugin.NetworkInterfaceSpec,
	machineID string,
) (*plugin.NetworkInterfaceStatus, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("nicName", spec.Name)

	log.V(1).Info("Reconciling nic")

	log.V(2).Info("Writing network interface dir")
	if err := os.MkdirAll(p.host.MachineNetworkInterfaceDir(machineID, spec.Name), os.ModePerm); err != nil {
		return nil, err
	}

	apinetNamespace, apinetNetworkName, _, _, err := provider.ParseNetworkID(spec.NetworkId)
	if err != nil {
		return nil, fmt.Errorf("error parsing ApiNet NetworkID %s: %w", spec.NetworkId, err)
	}

	log.V(2).Info("Writing APINet network interface config file")
	if err := p.writeAPINetNetworkInterfaceConfig(machineID, spec.Name, &apiNetNetworkInterfaceConfig{
		Namespace: apinetNamespace,
	}); err != nil {
		return nil, err
	}

	apinetNicKey := client.ObjectKey{
		Namespace: apinetNamespace,
		Name:      p.APInetNicName(machineID, spec.Name),
	}

	apinetNicCfg := apinetv1alpha1apply.NetworkInterface(apinetNicKey.Name, apinetNicKey.Namespace).
		WithSpec(apinetv1alpha1apply.NetworkInterfaceSpec().
			WithNetworkRef(corev1.LocalObjectReference{
				Name: apinetNetworkName,
			}).
			WithNodeRef(corev1.LocalObjectReference{
				Name: p.nodeName,
			}).
			WithHostname(spec.Hostname).
			WithIPs(ironcoreIPsToAPInetIPs(spec.Ips)...),
		)

	log.V(2).Info("Applying apinet nic")
	if err := p.apinetClient.Apply(ctx, apinetNicCfg, fieldOwner, client.ForceOwnership); err != nil {
		return nil, fmt.Errorf("error applying apinet network interface: %w", err)
	}

	apinetNic := &apinetv1alpha1.NetworkInterface{}
	if err := p.apinetClient.Get(ctx, apinetNicKey, apinetNic); err != nil {
		return nil, fmt.Errorf("error fetching apinet network interface %s: %w", apinetNicKey, err)
	}

	if apinetNic.Status.State == apinetv1alpha1.NetworkInterfaceStateReady {
		return getPluginStatus(apinetNic)
	}

	log.V(2).Info("Waiting for apinet network interface to become ready")
	if err := wait.PollUntilContextTimeout(
		ctx,
		500*time.Millisecond,
		5*time.Second,
		true,
		func(ctx context.Context) (done bool, err error) {
			if err := p.apinetClient.Get(ctx, apinetNicKey, apinetNic); err != nil {
				return false, fmt.Errorf("error getting apinet nic %s: %w", apinetNicKey, err)
			}

			return apinetNic.Status.State == apinetv1alpha1.NetworkInterfaceStateReady ||
				apinetNic.Status.State == apinetv1alpha1.NetworkInterfaceStateError, nil
		}); err != nil {
		return nil, fmt.Errorf("error waiting for nic to become ready: %w", err)
	}

	// Fetch the updated object to get the ID or any other updated fields
	if err := p.apinetClient.Get(ctx, apinetNicKey, apinetNic); err != nil {
		return nil, fmt.Errorf("error fetching updated apinet network interface: %w", err)
	}

	return getPluginStatus(apinetNic)
}

func getPluginStatus(nic *apinetv1alpha1.NetworkInterface) (*plugin.NetworkInterfaceStatus, error) {
	switch nic.Status.State {
	case apinetv1alpha1.NetworkInterfaceStatePending:
		return &plugin.NetworkInterfaceStatus{State: plugin.NetworkInterfaceStatePending}, nil
	case apinetv1alpha1.NetworkInterfaceStateError:
		return &plugin.NetworkInterfaceStatus{State: plugin.NetworkInterfaceStateError}, nil
	case apinetv1alpha1.NetworkInterfaceStateReady:
		pci, tap, err := getDeviceInfo(&nic.Status)
		if err != nil {
			return nil, fmt.Errorf("error getting device info: %w", err)
		}
		ips, err := toNetipAddrs(nic.Spec.IPs)
		if err != nil {
			return nil, fmt.Errorf("error parsing IPs: %w", err)
		}
		prefix, err := toNetipPrefix(nic.Status.Prefixes)
		if err != nil {
			return nil, fmt.Errorf("error parsing prefixes: %w", err)
		}
		virtualIP, err := virtualIP(nic)
		if err != nil {
			return nil, fmt.Errorf("error parsing virtual IP: %w", err)
		}

		return &plugin.NetworkInterfaceStatus{
			Handle: provider.GetNetworkInterfaceID(
				nic.Namespace,
				nic.Name,
				nic.Spec.NodeRef.Name,
				nic.UID,
			),
			State:     plugin.NetworkInterfaceStateReady,
			Ips:       ips,
			Prefixes:  prefix,
			VirtualIp: virtualIP,
			PCI:       pci,
			TAP:       tap,
		}, nil
	default:
		return nil, fmt.Errorf("unknown APINet network interface state %q", nic.Status.State)
	}
}

func toNetipAddrs[S fmt.Stringer](ss []S) ([]netip.Addr, error) {
	ret := make([]netip.Addr, 0, len(ss))
	for i, s := range ss {
		addr, err := netip.ParseAddr(s.String())
		if err != nil {
			return nil, fmt.Errorf("invalid IP at index %d: %w", i, err)
		}
		ret = append(ret, addr)
	}
	return ret, nil
}

func toNetipPrefix[S fmt.Stringer](ss []S) (netip.Prefix, error) {
	if len(ss) > 1 {
		return netip.Prefix{}, fmt.Errorf("got %d prefixes, want at most one", len(ss))
	}
	if len(ss) == 0 {
		return netip.Prefix{}, nil
	}
	prefix, err := netip.ParsePrefix(ss[0].String())
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid prefix: %w", err)
	}
	return prefix, nil
}

func virtualIP(nic *apinetv1alpha1.NetworkInterface) (netip.Addr, error) {
	if len(nic.Status.PublicIPs) == 0 {
		return netip.Addr{}, nil
	}
	addr, err := netip.ParseAddr(nic.Status.PublicIPs[0].String())
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid virtual IP: %w", err)
	}
	return addr, nil
}

func getDeviceInfo(status *apinetv1alpha1.NetworkInterfaceStatus) (*plugin.PCI, *plugin.TAP, error) {
	if status.PCIAddress != nil && status.TAPDevice != nil {
		return nil, nil, fmt.Errorf("both TAP device and PCI address found")
	}
	if status.PCIAddress != nil {
		pciDevice := status.PCIAddress
		pciAddress := fmt.Sprintf("%s:%s:%s.%s",
			pciDevice.Domain,
			pciDevice.Bus,
			pciDevice.Slot,
			pciDevice.Function,
		)

		return &plugin.PCI{BDF: pciAddress}, nil, nil
	}

	if status.TAPDevice != nil {
		return nil, &plugin.TAP{Name: status.TAPDevice.Name}, nil
	}

	return nil, nil, fmt.Errorf("no TAP device or PCI address found")
}

func (p *Plugin) Delete(ctx context.Context, computeNicName string, machineID string) error {
	log := ctrl.LoggerFrom(ctx)

	log.V(2).Info("Reading APINet network interface config file")
	cfg, err := p.readAPINetNetworkInterfaceConfig(machineID, computeNicName)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("error reading namespace file: %w", err)
		}

		log.V(2).Info("No namespace file found, deleting network interface dir")
		return os.RemoveAll(p.host.MachineNetworkInterfaceDir(machineID, computeNicName))
	}

	apinetNicKey := client.ObjectKey{
		Namespace: cfg.Namespace,
		Name:      p.APInetNicName(machineID, computeNicName),
	}
	log = log.WithValues("APInetNetworkInterfaceKey", apinetNicKey)

	if err := p.apinetClient.Delete(ctx, &apinetv1alpha1.NetworkInterface{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: apinetNicKey.Namespace,
			Name:      apinetNicKey.Name,
		},
	}); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("error deleting apinet network interface %s: %w", apinetNicKey, err)
		}

		log.V(1).Info("APInet network interface is already gone, removing network interface directory")
		return os.RemoveAll(p.host.MachineNetworkInterfaceDir(machineID, computeNicName))
	}

	log.V(1).Info("Waiting until apinet network interface is gone")
	if err := wait.PollUntilContextTimeout(
		ctx, 500*time.Millisecond,
		10*time.Second,
		true,
		func(ctx context.Context) (done bool, err error) {
			if err := p.apinetClient.Get(ctx, apinetNicKey, &apinetv1alpha1.NetworkInterface{}); err != nil {
				if !apierrors.IsNotFound(err) {
					return false, fmt.Errorf("error getting apinet network interface %s: %w", apinetNicKey, err)
				}
				return true, nil
			}
			return false, nil
		}); err != nil {
		return fmt.Errorf("error waiting for apinet network interface %s to be gone: %w", apinetNicKey, err)
	}

	log.V(1).Info("APInet network interface is gone, removing network interface dir")
	return os.RemoveAll(p.host.MachineNetworkInterfaceDir(machineID, computeNicName))
}

func (p *Plugin) Name() string {
	return pluginAPInet
}
