// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package cloud_hypervisor

// To fetch latest openapi spec:
// nolint:lll
// curl -s https://raw.githubusercontent.com/cloud-hypervisor/cloud-hypervisor/master/vmm/src/api/openapi/cloud-hypervisor.yaml -O

//go:generate bash -c "mkdir -p client && cat ./cloud-hypervisor.yaml | ../bin/oapi-codegen -package=client -generate=types,client,spec -o=./client/client.go /dev/stdin"
