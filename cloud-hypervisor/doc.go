// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package cloud_hypervisor

// To fetch latest openapi spec:
// nolint:lll
// curl -s https://raw.githubusercontent.com/cloud-hypervisor/cloud-hypervisor/master/vmm/src/api/openapi/cloud-hypervisor.yaml -O
//
// NOTE: cloud-hypervisor.yaml contains a LOCAL ADDITION for the fw_cfg device
// (FwCfgConfig/FwCfgItemList/FwCfgItem schemas + PayloadConfig.fw_cfg_config).
// fw_cfg is feature-gated in cloud-hypervisor and is absent from the upstream
// spec, so the curl command above will drop it. Re-apply those schemas after any
// refresh, then regenerate.

//go:generate bash -c "mkdir -p client && cat ./cloud-hypervisor.yaml | ../bin/oapi-codegen -package=client -generate=types,client,spec -o=./client/client.go /dev/stdin"
