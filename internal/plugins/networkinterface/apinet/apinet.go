// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package apinet

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/networkinterface"
	apinetv1alpha1 "github.com/ironcore-dev/ironcore-net/api/core/v1alpha1"
	apinet "github.com/ironcore-dev/ironcore-net/apimachinery/api/net"
	"github.com/ironcore-dev/ironcore-net/apinetlet/provider"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
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

func NewPlugin(nodeName string, client client.Client) networkinterface.Plugin {
	return &Plugin{
		nodeName:     nodeName,
		apinetClient: client,
	}
}

func (p *Plugin) Init(host host.Paths) error {
	p.host = host
	return nil
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
	spec *api.NetworkInterfaceSpec,
	machineID string,
) (*api.NetworkInterfaceStatus, error) {
	log := ctrl.LoggerFrom(ctx)

	log.V(1).Info("Writing network interface dir")
	if err := os.MkdirAll(p.host.MachineNetworkInterfaceDir(machineID, spec.Name), os.ModePerm); err != nil {
		return nil, err
	}

	apinetNamespace, apinetNetworkName, _, _, err := provider.ParseNetworkID(spec.NetworkId)
	if err != nil {
		return nil, fmt.Errorf("error parsing ApiNet NetworkID %s: %w", spec.NetworkId, err)
	}

	log.V(1).Info("Writing APINet network interface config file")
	if err := p.writeAPINetNetworkInterfaceConfig(machineID, spec.Name, &apiNetNetworkInterfaceConfig{
		Namespace: apinetNamespace,
	}); err != nil {
		return nil, err
	}

	apinetNic := &apinetv1alpha1.NetworkInterface{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apinetv1alpha1.SchemeGroupVersion.String(),
			Kind:       "NetworkInterface",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: apinetNamespace,
			Name:      p.APInetNicName(machineID, spec.Name),
		},
		Spec: apinetv1alpha1.NetworkInterfaceSpec{
			NetworkRef: corev1.LocalObjectReference{
				Name: apinetNetworkName,
			},
			NodeRef: corev1.LocalObjectReference{
				Name: p.nodeName,
			},
			IPs: ironcoreIPsToAPInetIPs(spec.Ips),
		},
	}

	log.V(1).Info("Applying apinet nic")
	if err := p.apinetClient.Patch(ctx, apinetNic, client.Apply, fieldOwner, client.ForceOwnership); err != nil {
		return nil, fmt.Errorf("error applying apinet network interface: %w", err)
	}

	pciAddress, err := getPCIAddress(apinetNic)
	if err != nil {
		return nil, fmt.Errorf("error getting host device: %w", err)
	}
	if pciAddress != nil {
		log.V(1).Info("Host device is ready", "HostDevice", pciAddress)
		return &api.NetworkInterfaceStatus{
			Handle: provider.GetNetworkInterfaceID(
				apinetNic.Namespace,
				apinetNic.Name,
				apinetNic.Spec.NodeRef.Name,
				apinetNic.UID,
			),
			Path:  fmt.Sprintf("/sys/bus/pci/devices/%s", ptr.Deref(pciAddress, "")),
			State: api.NetworkInterfaceStateAttached,
		}, nil
	}

	log.V(1).Info("Waiting for apinet network interface to become ready")
	apinetNicKey := client.ObjectKeyFromObject(apinetNic)
	if err := wait.PollUntilContextTimeout(
		ctx,
		50*time.Millisecond,
		5*time.Second,
		true,
		func(ctx context.Context) (done bool, err error) {
			if err := p.apinetClient.Get(ctx, apinetNicKey, apinetNic); err != nil {
				return false, fmt.Errorf("error getting apinet nic %s: %w", apinetNicKey, err)
			}

			pciAddress, err = getPCIAddress(apinetNic)
			if err != nil {
				return false, fmt.Errorf("error getting host device: %w", err)
			}
			return pciAddress != nil, nil
		}); err != nil {
		return nil, fmt.Errorf("error waiting for nic to become ready: %w", err)
	}

	// Fetch the updated object to get the ID or any other updated fields
	if err := p.apinetClient.Get(ctx, apinetNicKey, apinetNic); err != nil {
		return nil, fmt.Errorf("error fetching updated apinet network interface: %w", err)
	}

	return &api.NetworkInterfaceStatus{
		Handle: provider.GetNetworkInterfaceID(
			apinetNic.Namespace,
			apinetNic.Name,
			apinetNic.Spec.NodeRef.Name,
			apinetNic.UID,
		),
		Path:  fmt.Sprintf("/sys/bus/pci/devices/%s", ptr.Deref(pciAddress, "")),
		State: api.NetworkInterfaceStateAttached,
	}, nil
}

func getPCIAddress(apinetNic *apinetv1alpha1.NetworkInterface) (*string, error) {
	switch apinetNic.Status.State {
	case apinetv1alpha1.NetworkInterfaceStateReady:
		switch {
		case apinetNic.Status.PCIAddress == nil && apinetNic.Status.TAPDevice == nil:
			return nil, fmt.Errorf("apinet network interface: PCIAddress and TAPDevice not set")
		case apinetNic.Status.PCIAddress == nil && apinetNic.Status.TAPDevice != nil:
			//TODO
			return nil, fmt.Errorf("not implemented")
		case apinetNic.Status.PCIAddress != nil && apinetNic.Status.TAPDevice == nil:
			pciDevice := apinetNic.Status.PCIAddress
			return ptr.To(fmt.Sprintf("%s:%s:%s.%s",
				pciDevice.Domain,
				pciDevice.Bus,
				pciDevice.Slot,
				pciDevice.Function,
			)), nil
		default:
			return nil, fmt.Errorf("apinet network interface: PCIAddress and TAPDevice should not be set at the same" +
				" time")
		}
	case apinetv1alpha1.NetworkInterfaceStatePending:
		return nil, nil
	case apinetv1alpha1.NetworkInterfaceStateError:
		return nil, fmt.Errorf("apinet network interface is in state error")
	default:
		return nil, nil
	}
}

func (p *Plugin) Delete(ctx context.Context, computeNicName string, machineID string) error {
	log := ctrl.LoggerFrom(ctx)

	log.V(1).Info("Reading APINet network interface config file")
	cfg, err := p.readAPINetNetworkInterfaceConfig(machineID, computeNicName)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("error reading namespace file: %w", err)
		}

		log.V(1).Info("No namespace file found, deleting network interface dir")
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
		ctx, 50*time.Millisecond,
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
