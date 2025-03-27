// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package ceph

import (
	"context"
	"errors"
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

	log.V(2).Info("Checking ceph conf")
	confPath, err := q.createCephConf(log, machineID, volume)
	if err != nil {
		return "", fmt.Errorf("error creating ceph conf: %w", err)
	}

	log.V(2).Info("Checking if daemon is running")
	running, err := q.isDaemonRunning(machineID, volume.handle)
	if err != nil {
		return "", fmt.Errorf("error checking if daemon is running: %w", err)
	}

	if !present || !running {
		log.V(1).Info("Starting qemu-storage-daemon")
		if err := q.startDaemon(ctx, log, machineID, socketPath, confPath, volume); err != nil {
			return "", fmt.Errorf("error starting qemu-storage-daemon: %w", err)
		}
	}

	return socketPath, nil
}

func (q *QemuStorage) Unmount(ctx context.Context, machineID, volumeID string) error {
	log := q.log.WithValues("machineID", machineID, "volumeID", volumeID)

	log.V(1).Info("Check if deamon is running")
	running, err := q.isDaemonRunning(machineID, volumeID)
	if err != nil {
		return fmt.Errorf("error checking if deamon is running: %w", err)
	}

	if !running {
		log.V(1).Info("Deamon is not running, done")
		return nil
	}

	log.V(1).Info("Stop deamon")
	if err := q.stopDaemon(machineID, volumeID); err != nil {
		return fmt.Errorf("error stopping deamon: %w", err)
	}

	return nil
}

func (q *QemuStorage) createCephConf(log logr.Logger, machineID string, volume *validatedVolume) (string, error) {
	confPath := filepath.Join(
		q.paths.MachineVolumeDir(machineID, utilstrings.EscapeQualifiedName(pluginName), volume.handle),
		"ceph.conf",
	)
	keyPath := filepath.Join(
		q.paths.MachineVolumeDir(machineID, utilstrings.EscapeQualifiedName(pluginName), volume.handle),
		"ceph.key",
	)

	log.V(2).Info("Creating ceph conf", "confPath", confPath)
	confFile, err := os.OpenFile(confPath, os.O_CREATE|os.O_WRONLY, os.ModePerm)
	if err != nil {
		return "", fmt.Errorf("error opening conf file %s: %w", confPath, err)
	}

	confData := fmt.Sprintf(
		"[global]\nmon_host = %s \n\n[client.%s]\nkeyring = %s\n",
		strings.Join(volume.monitors, ","),
		volume.userID,
		"./ceph.key",
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

	keyData := fmt.Sprintf("[client.%s]\nkey = %s\n", volume.userID, volume.userKey)
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
			"./ceph.conf",
		),
		"--export",
		fmt.Sprintf(
			"vhost-user-blk,id=%s,node-name=%s,addr.type=unix,addr.path=%s,writable=on",
			"rbd0",
			"rbd0",
			"./socket",
		),
	}

	log.V(1).Info("Start qemu-storage-daemon", "cmd", cmd)
	process := exec.Command(cmd[0], cmd[1:]...)
	process.Dir = q.paths.MachineVolumeDir(machineID, utilstrings.EscapeQualifiedName(pluginName), volume.handle)

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
		if process.Process != nil {
			if err := process.Process.Kill(); err != nil {
				log.V(1).Info("failed to kill qemu-storage-daemon")
			}
		}

		return fmt.Errorf("error waiting for socket: %w", err)
	}

	pidPath := q.pidFilePath(machineID, volume.handle)
	pidFile, err := os.OpenFile(pidPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("error opening conf file %s: %w", pidPath, err)
	}

	if _, err := fmt.Fprintf(pidFile, "%d", process.Process.Pid); err != nil {
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

func getPidFromFile(pidPath string) (int, error) {
	pidFile, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, fmt.Errorf("error opening conf file %s: %w", pidPath, err)
	}

	pid, err := strconv.Atoi(string(pidFile))
	if err != nil {
		return 0, fmt.Errorf("error parsing pid file %s: %w", pidPath, err)
	}

	return pid, nil
}

func (q *QemuStorage) isDaemonRunning(machineID, volumeHandle string) (bool, error) {
	pid, err := getPidFromFile(q.pidFilePath(machineID, volumeHandle))
	if err != nil {
		return false, fmt.Errorf("faild to get pid from file: %w", err)
	}

	p, err := os.FindProcess(pid)
	if err != nil {
		return false, nil
	}

	if err := p.Signal(syscall.Signal(0)); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return false, nil
		}
		return false, fmt.Errorf("failed to signal process: %w", err)
	}

	return true, nil
}

func (q *QemuStorage) stopDaemon(machineID, volumeHandle string) error {
	pid, err := getPidFromFile(q.pidFilePath(machineID, volumeHandle))
	if err != nil {
		return fmt.Errorf("faild to get pid from file: %w", err)
	}

	p, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}

	if err := p.Kill(); err != nil {
		return fmt.Errorf("faild to kill process: %w", err)
	}

	return nil
}
