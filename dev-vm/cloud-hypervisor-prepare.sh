#!/usr/bin/env bash
set -Eeuo pipefail
export DEBIAN_FRONTEND=noninteractive

CH_RELEASE_REPO="${CH_RELEASE_REPO:-ironcore-dev/cloud-hypervisor-provider}"
CH_RELEASE_TAG="${CH_RELEASE_TAG:-cloud-hypervisor-fw_cfg}"

EDK2_FW_TAG="${EDK2_FW_TAG:-ch-97eeb7b09}"

case "$(uname -m)" in
  x86_64)  SUFFIX="";         FW_ASSET="CLOUDHV.fd" ;;
  aarch64) SUFFIX="-aarch64"; FW_ASSET="CLOUDHV_EFI.fd" ;;
  *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;;
esac

CH_BASE_URL="https://github.com/${CH_RELEASE_REPO}/releases/download/${CH_RELEASE_TAG}"
FW_BASE_URL="https://github.com/cloud-hypervisor/edk2/releases/download/${EDK2_FW_TAG}"

curl -fL "${CH_BASE_URL}/cloud-hypervisor-static${SUFFIX}" -o cloud-hypervisor
sudo install -m 0755 cloud-hypervisor /usr/local/bin/cloud-hypervisor
rm -f cloud-hypervisor

sudo curl -fL "${FW_BASE_URL}/${FW_ASSET}" -o /usr/local/bin/CLOUDHV_EFI.fd
sudo chmod 0644 /usr/local/bin/CLOUDHV_EFI.fd

/usr/local/bin/cloud-hypervisor --version
/usr/local/bin/cloud-hypervisor --help | grep -q fw-cfg-config \
  || echo "WARNING: cloud-hypervisor was not built with --features fw_cfg" >&2
