//go:build darwin

package process

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/go-logr/logr"
)

func SpawnDetached(log logr.Logger, bin string, args []string, preFunc func(cmd *exec.Cmd), postFunc func(pid int) error) error {
	log.V(1).Info("Start cloud-hypervisor (detached not supported on darwin)", "bin", bin, "args", strings.Join(args, " "))

	cmd := exec.Command(bin, args...)
	cmd.Stdout = os.Stdout // Print output directly to console
	cmd.Stderr = os.Stderr // Print errors directly to console

	if preFunc != nil {
		preFunc(cmd)
	}

	log.V(1).Info("Starting vmm")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to init cloud-hypervisor: %w", err)
	}

	if postFunc == nil {
		return nil
	}

	if cmd.Process != nil {
		if err := postFunc(cmd.Process.Pid); err != nil {
			return fmt.Errorf("failed run post exec func: %w", err)
		}
	}

	return nil
}
