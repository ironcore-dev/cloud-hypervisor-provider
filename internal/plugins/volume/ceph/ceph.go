// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package ceph

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/digitalocean/go-qemu/qmp"
	"github.com/go-logr/logr"
	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/volume"
	"k8s.io/utils/ptr"
)

const (
	pluginName = "cloud-hypervisor-provider.ironcore.dev/ceph"

	cephDriverName = "ceph"

	volumeAttributeImageKey     = "image"
	volumeAttributesMonitorsKey = "monitors"

	secretUserIDKey  = "userID"
	secretUserKeyKey = "userKey"

	secretEncryptionKey = "encryptionKey"
)

type validatedVolume struct {
	name          string
	monitors      []string
	pool          string
	image         string
	handle        string
	userID        string
	userKey       string
	encryptionKey *string
}

type Provider interface {
	Mount(ctx context.Context, machineID string, volume *validatedVolume) (string, error)
	Unmount(ctx context.Context, machineID string, volumeID string) error
}

func QMPProvider(ctx context.Context, log logr.Logger, paths host.Paths, socket string) (Provider, error) {
	monitor, err := qmp.NewSocketMonitor("unix", socket, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to qmp monitor: %w", err)
	}

	go func() {
		// TODO
		_ = monitor.Connect()
		defer func() {
			// TODO
			_ = monitor.Disconnect()
		}()

		stream, _ := monitor.Events(ctx)
		for e := range stream {
			log.V(1).Info(fmt.Sprintf("EVENT: %s", e.Event))
		}
	}()

	return &QMP{
		log:     log,
		paths:   paths,
		monitor: monitor,
	}, nil
}

type plugin struct {
	provider Provider
	host     volume.Host
}

func NewPlugin(provider Provider) volume.Plugin {
	return &plugin{
		provider: provider,
	}
}

func (p *plugin) Init(host volume.Host) error {
	p.host = host
	return nil
}

func (p *plugin) Name() string {
	return pluginName
}

func (p *plugin) GetBackingVolumeID(spec *api.VolumeSpec) (string, error) {
	storage := spec.Connection
	if storage == nil {
		return "", fmt.Errorf("volume is nil")
	}

	handle := storage.Handle
	if handle == "" {
		return "", fmt.Errorf("volume access does not specify handle: %s", handle)
	}

	return fmt.Sprintf("%s^%s", pluginName, handle), nil
}

func (p *plugin) CanSupport(spec *api.VolumeSpec) bool {
	storage := spec.Connection
	if storage == nil {
		return false
	}

	return storage.Driver == cephDriverName
}

func readSecretData(data map[string][]byte) (userID, userKey string, err error) {
	userIDData, ok := data[secretUserIDKey]
	if !ok || len(userIDData) == 0 {
		return "", "", fmt.Errorf("no user id at %s", secretUserIDKey)
	}

	userKeyData, ok := data[secretUserKeyKey]
	if !ok || len(userKeyData) == 0 {
		return "", "", fmt.Errorf("no user key at %s", secretUserKeyKey)
	}

	return string(userIDData), string(userKeyData), nil
}

func readEncryptionData(data map[string][]byte) (*string, error) {
	encryptionKey, ok := data[secretEncryptionKey]
	if !ok || len(encryptionKey) == 0 {
		return nil, fmt.Errorf("no encryption key at %s", secretEncryptionKey)
	}

	return ptr.To(string(encryptionKey)), nil
}

func readVolumeAttributes(attrs map[string]string, volumeData *validatedVolume) (err error) {
	monitorsString, ok := attrs[volumeAttributesMonitorsKey]
	if !ok || monitorsString == "" {
		return fmt.Errorf("no monitors data at %s", volumeAttributesMonitorsKey)
	}

	var monitors []string
	for _, monitor := range strings.Split(monitorsString, ",") {
		// check format
		if _, _, err := net.SplitHostPort(monitor); err != nil {
			return fmt.Errorf("[monitor %s] error splitting host / port: %w", monitor, err)
		}

		monitors = append(monitors, monitor)
	}

	imageAndPool, ok := attrs[volumeAttributeImageKey]
	if !ok || imageAndPool == "" {
		return fmt.Errorf("no image data at %s", volumeAttributeImageKey)
	}

	split := strings.Split(imageAndPool, "/")
	if len(split) != 2 {
		return fmt.Errorf("invalid image format: %s", imageAndPool)
	}

	volumeData.monitors = monitors
	volumeData.image = split[1]
	volumeData.pool = split[0]

	return nil
}

func (p *plugin) Apply(ctx context.Context, spec *api.VolumeSpec, machineID string) (*api.VolumeStatus, error) {
	volumeData, err := p.validateVolume(spec)
	if err != nil {
		return nil, fmt.Errorf("failed to get volume data: %w", err)
	}

	path, err := p.provider.Mount(ctx, machineID, volumeData)
	if err != nil {
		return nil, fmt.Errorf("failed to mount volume: %w", err)
	}

	return &api.VolumeStatus{
		Name:   spec.Name,
		Type:   api.VolumeSocketType,
		Path:   path,
		Handle: volumeData.handle,
		State:  api.VolumeStatePrepared,
	}, nil
}

func (p *plugin) validateVolume(spec *api.VolumeSpec) (vData *validatedVolume, err error) {
	connection := spec.Connection
	if connection == nil {
		return nil, fmt.Errorf("volume does not specify connection")
	}
	if connection.Driver != cephDriverName {
		return nil, fmt.Errorf("volume connection specifies invalid driver %q", connection.Driver)
	}
	if connection.Attributes == nil {
		return nil, fmt.Errorf("volume connection does not specify attributes")
	}
	if connection.SecretData == nil {
		return nil, fmt.Errorf("volume connection does not specify secret data")
	}
	if connection.Handle == "" {
		return nil, fmt.Errorf("volume connection does not specify handle")
	}

	vData = &validatedVolume{
		name:   spec.Name,
		handle: connection.Handle,
	}

	if err := readVolumeAttributes(connection.Attributes, vData); err != nil {
		return nil, fmt.Errorf("error reading volume attributes: %w", err)
	}

	vData.userID, vData.userKey, err = readSecretData(connection.SecretData)
	if err != nil {
		return nil, fmt.Errorf("error reading secret data: %w", err)
	}

	if encryptionData := spec.Connection.EncryptionData; encryptionData != nil {
		vData.encryptionKey, err = readEncryptionData(encryptionData)
		if err != nil {
			return nil, fmt.Errorf("error reading encryption data: %w", err)
		}
	}

	return vData, nil
}

func (p *plugin) Delete(ctx context.Context, computeVolumeName string, machineID string) error {
	if err := p.provider.Unmount(ctx, machineID, computeVolumeName); err != nil {
		return fmt.Errorf("failed to unmount volume %q: %w", computeVolumeName, err)
	}

	return os.RemoveAll(p.host.MachineVolumeDir(machineID, cephDriverName, computeVolumeName))
}
