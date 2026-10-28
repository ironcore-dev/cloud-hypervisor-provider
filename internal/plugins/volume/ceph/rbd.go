// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package ceph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
)

type rbdProvider struct {
	log   logr.Logger
	paths host.Paths
}

func newRBDProvider(log logr.Logger, paths host.Paths) *rbdProvider {
	return &rbdProvider{
		log:   log,
		paths: paths,
	}
}

func (r *rbdProvider) VolumeType() api.VolumeType {
	return api.VolumeFileType
}

func (r *rbdProvider) Mount(ctx context.Context, machineID string, volume *validatedVolume) (string, error) {
	volumeDir := r.volumeDir(machineID, volume.name)
	if err := os.MkdirAll(volumeDir, 0o700); err != nil {
		return "", err
	}

	log := r.log.WithValues("machineID", machineID, "volumeID", volume.handle)

	log.V(2).Info("Creating ceph conf")
	confPath, err := r.createCephConf(log, machineID, volume)
	if err != nil {
		return "", fmt.Errorf("error creating ceph conf: %w", err)
	}

	devicePath, err := r.findMappedDevice(volume.pool, volume.image)
	if err != nil {
		return "", fmt.Errorf("error checking existing rbd mappings: %w", err)
	}

	if devicePath != "" {
		log.V(1).Info("RBD image already mapped", "device", devicePath)
		return devicePath, nil
	}

	devicePath, err = r.mapDevice(ctx, volume, confPath)
	if err != nil {
		return "", fmt.Errorf("error mapping rbd device: %w", err)
	}

	log.V(1).Info("Mapped RBD image to device", "device", devicePath)
	return devicePath, nil
}

func (r *rbdProvider) Unmount(ctx context.Context, machineID string, volume *validatedVolume) error {
	log := r.log.WithValues("machineID", machineID, "volumeName", volume.name)

	device, err := r.findMappedDevice(volume.pool, volume.image)
	if err != nil {
		return fmt.Errorf("error querying rbd mappings: %w", err)
	}

	if device == "" {
		log.V(1).Info("RBD image not currently mapped, nothing to unmap", "pool", volume.pool, "image", volume.image)
		return nil
	}

	log.V(1).Info("Unmapping RBD device", "device", device)
	if err := r.unmapDevice(ctx, device); err != nil {
		return fmt.Errorf("error unmapping rbd device %s: %w", device, err)
	}
	return nil
}

func (r *rbdProvider) volumeDir(machineID string, volumeHandle string) string {
	return r.paths.MachineVolumeDir(machineID, cephDriverName, volumeHandle)
}

func (r *rbdProvider) createCephConf(log logr.Logger, machineID string, volume *validatedVolume) (string, error) {
	confPath := filepath.Join(
		r.volumeDir(machineID, volume.name),
		"ceph.conf",
	)
	keyPath := filepath.Join(
		r.volumeDir(machineID, volume.name),
		"ceph.key",
	)

	log.V(2).Info("Creating ceph conf", "confPath", confPath)
	confData := fmt.Sprintf(
		"[global]\nmon_host = %s \n\n[client.%s]\nkeyring = %s\n",
		strings.Join(volume.monitors, ","),
		volume.userID,
		keyPath,
	)
	if err := os.WriteFile(confPath, []byte(confData), 0o600); err != nil {
		return "", fmt.Errorf("error writing conf file %s: %w", confPath, err)
	}
	if err := os.Chmod(confPath, 0o600); err != nil {
		return "", fmt.Errorf("error setting permissions on conf file %s: %w", confPath, err)
	}

	log.V(1).Info("Creating ceph key", "keyPath", keyPath)
	keyData := fmt.Sprintf("[client.%s]\nkey = %s\n", volume.userID, volume.userKey)
	if err := os.WriteFile(keyPath, []byte(keyData), 0o600); err != nil {
		return "", fmt.Errorf("error writing key file %s: %w", keyPath, err)
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		return "", fmt.Errorf("error setting permissions on key file %s: %w", keyPath, err)
	}

	return confPath, nil
}

func (r *rbdProvider) mapDevice(ctx context.Context, volume *validatedVolume, confPath string) (string, error) {
	imageSpec := fmt.Sprintf("%s/%s", volume.pool, volume.image)

	args := []string{
		"map", imageSpec,
		"--id", volume.userID,
		"-c", confPath,
	}

	cmd := exec.CommandContext(ctx, "rbd", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("rbd map failed: %w, stderr: %s", err, stderr.String())
	}

	devicePath := strings.TrimSpace(stdout.String())
	if devicePath == "" {
		return "", fmt.Errorf("rbd map returned empty device path")
	}

	return devicePath, nil
}

func (r *rbdProvider) unmapDevice(ctx context.Context, devicePath string) error {
	cmd := exec.CommandContext(ctx, "rbd", "unmap", devicePath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rbd unmap failed: %w, stderr: %s", err, stderr.String())
	}

	return nil
}

// rbdMappedEntry represents a single device from `rbd showmapped --format json`.
type rbdMappedEntry struct {
	Pool      string `json:"pool"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Snap      string `json:"snap"`
	Device    string `json:"device"`
}

// findMappedDevice returns the kernel device path for an already-mapped RBD
// image, or "" if the image is not currently mapped.
func (r *rbdProvider) findMappedDevice(pool, image string) (string, error) {
	output, err := exec.Command("rbd", "showmapped", "--format", "json").Output()
	if err != nil {
		return "", fmt.Errorf("rbd showmapped failed: %w", err)
	}

	if len(bytes.TrimSpace(output)) == 0 {
		return "", nil
	}

	var mapped []rbdMappedEntry
	if err := json.Unmarshal(output, &mapped); err != nil {
		return "", fmt.Errorf("error parsing rbd showmapped output: %w", err)
	}

	for _, entry := range mapped {
		if entry.Pool == pool && entry.Name == image {
			return entry.Device, nil
		}
	}

	return "", nil
}
