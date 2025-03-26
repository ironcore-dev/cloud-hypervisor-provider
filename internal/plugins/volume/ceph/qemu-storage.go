// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package ceph

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"github.com/ironcore-dev/ironcore/broker/common"
	utilstrings "k8s.io/utils/strings"
)

type QemuStorage struct {
	log    logr.Logger
	paths  host.Paths
	bin    string
	detach bool
}

func (q *QemuStorage) Mount(ctx context.Context, machineID string, volume *validatedVolume) (string, error) {
	volumeDir := q.paths.MachineVolumeDir(machineID, utilstrings.EscapeQualifiedName(pluginName), volume.handle)
	if err := os.MkdirAll(volumeDir, os.ModePerm); err != nil {
		return "", err
	}

	log := q.log.WithValues("machineID", machineID, "volumeID", volume.handle)
	socketPath := filepath.Join(volumeDir, "socket")

	log.V(2).Info("Checking if socket is present", "path", socketPath)
	present, err := isSocketPresent(socketPath)
	if err != nil {
		return "", fmt.Errorf("error checking if %s is a socket: %w", socketPath, err)
	}

	var active bool
	if present {
		log.V(2).Info("Checking if socket is active", "path", socketPath)
		active, err = isSocketActive(socketPath)
		if err != nil {
			return "", fmt.Errorf("error checking if %s is a active socket: %w", socketPath, err)
		}
	}

	log.V(2).Info("Checking ceph conf")
	confPath, err := q.createCephConf(log, machineID, volume)
	if err != nil {
		return "", fmt.Errorf("error creating ceph conf: %w", err)
	}

	if !present || !active {
		log.V(1).Info("qemu-storage-daemon socket is not present, create it", "path", socketPath)
		if err := q.startDaemon(ctx, log, machineID, socketPath, confPath, volume); err != nil {
			return "", fmt.Errorf("error starting qemu-storage-daemon: %w", err)
		}
	}

	return socketPath, nil
}

func (q *QemuStorage) createCephConf(log logr.Logger, machineID string, volume *validatedVolume) (string, error) {
	confPath := filepath.Join(
		q.paths.MachineVolumeDir(machineID, utilstrings.EscapeQualifiedName(pluginName), volume.handle),
		"conf",
	)
	keyPath := filepath.Join(
		q.paths.MachineVolumeDir(machineID, utilstrings.EscapeQualifiedName(pluginName), volume.handle),
		"key",
	)

	log.V(2).Info("Creating ceph conf", "confPath", confPath)
	confFile, err := os.OpenFile(confPath, os.O_CREATE|os.O_WRONLY, os.ModePerm)
	if err != nil {
		return "", fmt.Errorf("error opening conf file %s: %w", confPath, err)
	}

	confData := fmt.Sprintf(
		"[global]\nmon_host = %s \n\n[client.%s]\nkeyring = %s",
		strings.Join(volume.monitors, ","),
		volume.userID,
		"./key",
	)
	_, err = confFile.WriteString(confData)
	if err != nil {
		return "", fmt.Errorf("error writing to conf file %s: %w", confPath, err)
	}

	log.V(1).Info("Creating ceph key", "keyPath", keyPath)
	keyFile, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY, os.ModePerm)
	if err != nil {
		return "", fmt.Errorf("error opening key file %s: %w", keyPath, err)
	}

	keyData := fmt.Sprintf("[client.%s]\nkey = %s", volume.userID, volume.userKey)
	_, err = keyFile.WriteString(keyData)
	if err != nil {
		return "", fmt.Errorf("error writing to key file %s: %w", keyPath, err)
	}

	return confPath, nil
}

func (q *QemuStorage) startDaemon(
	ctx context.Context,
	log logr.Logger,
	machineID,
	socket,
	confPath string,
	volume *validatedVolume,
) error {
	log.V(2).Info("Cleaning up any previous socket")
	if err := common.CleanupSocketIfExists(socket); err != nil {
		return fmt.Errorf("error cleaning up socket: %w", err)
	}

	cmd := []string{
		q.bin,
		"--blockdev",
		fmt.Sprintf(
			"driver=rbd,node-name=%s,pool=%s,image=%s,discard=unmap,cache.direct=on,user=%s,conf=%s",
			"rbd0",
			volume.pool,
			volume.image,
			volume.userID,
			confPath,
		),
		"--export",
		fmt.Sprintf(
			"vhost-user-blk,id=%s,node-name=%s,addr.type=unix,addr.path=%s,writable=on",
			volume.handle,
			volume.handle,
			socket,
		),
	}

	log.V(1).Info("Start qemu-storage-daemon", "cmd", cmd)
	process := exec.Command(cmd[0], cmd[1:]...)

	if q.detach {
		process.SysProcAttr = &syscall.SysProcAttr{
			Setpgid: true,
		}
	}

	process.Stdout = os.Stdout // Print output directly to console
	process.Stderr = os.Stderr // Print errors directly to console

	log.V(1).Info("Starting qemu-storage-daemon")
	if err := process.Start(); err != nil {
		return fmt.Errorf("failed to start qemu-storage-daemon: %w", err)
	}

	log.V(2).Info("Wait for socket", "path", socket)
	if err := waitForSocketWithTimeout(ctx, 2*time.Second, socket); err != nil {
		return fmt.Errorf("error waiting for socket: %w", err)
	}

	pidPath := q.pidFilePath(machineID, volume.handle)
	pidFile, err := os.OpenFile(pidPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("error opening conf file %s: %w", pidPath, err)
	}

	if _, err := fmt.Fprintf(pidFile, "[client.%s]\n", volume.userID); err != nil {
		return fmt.Errorf("error writing to pid file %s: %w", confPath, err)
	}

	return nil
}

func (q *QemuStorage) pidFilePath(machineID, volumeHandle string) string {
	return filepath.Join(
		q.paths.MachineVolumeDir(machineID, utilstrings.EscapeQualifiedName(pluginName), volumeHandle),
		"pid",
	)
}

func (q *QemuStorage) Unmount(ctx context.Context, machineID, volumeID string) error {
	log := q.log.WithValues("machineID", machineID, "volumeID", volumeID)
	socketPath := filepath.Join(
		q.paths.MachineVolumeDir(machineID, utilstrings.EscapeQualifiedName(pluginName), volumeID),
		"socket",
	)

	log.V(2).Info("Checking if socket is present", "path", socketPath)
	present, err := isSocketPresent(socketPath)
	if err != nil {
		return fmt.Errorf("error checking if %s is a socket: %w", socketPath, err)
	}

	if !present {
		return nil
	}

	pidPath := q.pidFilePath(machineID, volumeID)
	pidFile, err := os.ReadFile(pidPath)
	if err != nil {
		return fmt.Errorf("error opening conf file %s: %w", pidPath, err)
	}

	pid, err := strconv.Atoi(string(pidFile))
	if err != nil {
		return fmt.Errorf("error parsing pid file %s: %w", pidPath, err)
	}

	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		return fmt.Errorf("error sending SIGKILL to %s: %w", socketPath, err)
	}

	return nil
}
