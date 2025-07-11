// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package vmm

import (
	"context"
	"net"
	"net/http"

	"github.com/ironcore-dev/cloud-hypervisor-provider/cloud-hypervisor/client"
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
