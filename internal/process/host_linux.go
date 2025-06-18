//go:build linux

package process

import (
	"fmt"
	"github.com/go-logr/logr"
	"golang.org/x/sys/unix"
	"os"
	"runtime"
	"strconv"
	"syscall"
)

func SpawnDetached(log logr.Logger, bin string, args []string, postFunc func(pid int) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	nsFile, err := os.Open("/proc/1/ns/pid")
	if err != nil {
		return fmt.Errorf("open ns: %w", err)
	}
	defer nsFile.Close()

	if err := unix.Setns(int(nsFile.Fd()), unix.CLONE_NEWPID); err != nil {
		return fmt.Errorf("failed to set ns: %w", err)
	}

	var SysFork uintptr
	switch runtime.GOARCH {
	case "amd64":
		SysFork = 57
	case "arm64", "riscv64":
		SysFork = 220
	default:
		return fmt.Errorf("unsupported architecture: %s", runtime.GOARCH)
	}

	pid, _, errno := syscall.RawSyscall(uintptr(SysFork), 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("fork failed: %w", errno)
	}

	if pid > 0 {
		log.V(2).Info("Spawned child PID", "pid", pid)
		return nil
	}

	_, err = syscall.Setsid()
	if err != nil {
		return fmt.Errorf("setsid failed: %w", err)
	}

	err = os.WriteFile("/sys/fs/cgroup/cgroup.procs", []byte(strconv.Itoa(os.Getpid())), 0644)
	if err != nil {
		return fmt.Errorf("write cgroups failed: %w", err)
	}

	if err := syscall.Exec(bin, append([]string{bin}, args...), os.Environ()); err != nil {
		return fmt.Errorf("exec failed: %w", err)
	}

	if postFunc == nil {
		return nil
	}

	if err := postFunc(int(pid)); err != nil {
		return fmt.Errorf("failed run post exec func: %w", err)
	}

	return nil
}
