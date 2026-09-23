// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package vmm

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// writePid atomically records pid in the machine's pidfile.
func (m *Manager) writePid(machineID string, pid int) error {
	path := m.paths.MachineChPidFile(machineID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return fmt.Errorf("failed to write pidfile: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("failed to rename pidfile: %w", err)
	}
	return nil
}

// readPid reads the machine's pidfile, returning false if it is absent or invalid.
func (m *Manager) readPid(machineID string) (int, bool) {
	data, err := os.ReadFile(m.paths.MachineChPidFile(machineID))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// removePid deletes the machine's pidfile, tolerating its absence.
func (m *Manager) removePid(machineID string) error {
	if err := os.Remove(m.paths.MachineChPidFile(machineID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// processAlive reports whether a process with the given pid currently exists.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Signal 0 performs error checking without delivering a signal.
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	// EPERM means the process exists but we may not signal it.
	return errors.Is(err, syscall.EPERM)
}
