// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package vmm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/ironcore-dev/cloud-hypervisor-provider/cloud-hypervisor/client"
	"k8s.io/apimachinery/pkg/util/wait"
)

func newUnixSocketClient(socketPath string) (*client.ClientWithResponses, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		},
	}

	httpClient := &http.Client{
		Transport: transport,
	}

	return client.NewClientWithResponses("http://localhost/api/v1", client.WithHTTPClient(httpClient))
}

func isSocketPresent(socketPath string) (bool, error) {
	stat, err := os.Stat(socketPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("error stat-ing socket %q: %w", socketPath, err)
		}
		return false, nil
	}

	if stat.Mode().Type()&os.ModeSocket == 0 {
		return false, fmt.Errorf("file at %s is not a socket", socketPath)
	}
	return true, nil
}

func isSocketActive(socketPath string) (bool, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return false, nil
		}
		return false, err
	}
	defer func() {
		_ = conn.Close()
	}()

	return true, nil
}

func waitForSocketWithTimeout(ctx context.Context, timeout time.Duration, apiSocket string) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := wait.PollUntilContextCancel(
		waitCtx, 500*time.Millisecond,
		true,
		func(ctx context.Context) (done bool, err error) {
			if stat, err := os.Stat(apiSocket); err == nil && stat.Mode().Type()&os.ModeSocket != 0 {
				return true, nil
			}
			return false, nil
		}); err != nil {
		return fmt.Errorf("vmm socket is not available: %w", err)
	}

	return nil
}
