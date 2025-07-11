// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package emptydisk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/volume"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/raw"
	utilstrings "k8s.io/utils/strings"
)

const (
	pluginName = "cloud-hypervisor-provider.ironcore.dev/empty-disk"

	defaultSize = 500 * 1024 * 1024 // 500Mi by default
)

type plugin struct {
	host volume.Host
	raw  raw.Raw
}

func NewPlugin(raw raw.Raw) volume.Plugin {
	return &plugin{
		raw: raw,
	}
}

func (p *plugin) Init(host volume.Host) error {
	p.host = host
	return nil
}

func (p *plugin) Name() string {
	return pluginName
}

func (p *plugin) GetBackingVolumeID(volume *api.VolumeSpec) (string, error) {
	if volume.EmptyDisk == nil {
		return "", fmt.Errorf("volume does not specify an EmptyDisk")
	}
	return volume.Name, nil
}

func (p *plugin) CanSupport(volume *api.VolumeSpec) bool {
	return volume.EmptyDisk != nil
}

func (p *plugin) diskFilename(computeVolumeName string, machineID string) string {
	return filepath.Join(
		p.host.MachineVolumeDir(machineID, utilstrings.EscapeQualifiedName(pluginName), computeVolumeName),
		"disk.raw",
	)
}

func (p *plugin) Apply(_ context.Context, spec *api.VolumeSpec, machineID string) (*api.VolumeStatus, error) {
	volumeDir := p.host.MachineVolumeDir(machineID, utilstrings.EscapeQualifiedName(pluginName), spec.Name)
	if err := os.MkdirAll(volumeDir, os.ModePerm); err != nil {
		return nil, err
	}

	handle, err := randomHex(8)
	if err != nil {
		return nil, fmt.Errorf("failed to generate WWN/handle for the disk: %w", err)
	}

	size := spec.EmptyDisk.Size
	if size == 0 {
		size = defaultSize
	}

	diskFilename := p.diskFilename(spec.Name, machineID)
	if _, err := os.Stat(diskFilename); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("error stat-ing disk: %w", err)
		}

		if err := p.raw.Create(diskFilename, raw.WithSize(size)); err != nil {
			return nil, fmt.Errorf("error creating disk %w", err)
		}
		if err := os.Chmod(diskFilename, os.FileMode(0666)); err != nil {
			return nil, fmt.Errorf("error changing disk file mode: %w", err)
		}
	}
	return &api.VolumeStatus{
		Name:   spec.Name,
		Type:   api.VolumeFileType,
		Path:   diskFilename,
		Handle: handle,
		State:  api.VolumeStatePrepared,
		Size:   size,
	}, nil
}

func (p *plugin) Delete(_ context.Context, computeVolumeName string, machineID string) error {
	return os.RemoveAll(p.host.MachineVolumeDir(machineID, utilstrings.EscapeQualifiedName(pluginName), computeVolumeName))
}

// randomHex generates random hexadecimal digits of the length n*2.
func randomHex(n int) (string, error) {
	bytes := make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
