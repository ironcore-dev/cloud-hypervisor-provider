#!/usr/bin/env bash
set -Eeuo pipefail
export DEBIAN_FRONTEND=noninteractive

CH_RELEASE_REPO="${CH_RELEASE_REPO:-ironcore-dev/cloud-hypervisor-provider}"
CH_RELEASE_TAG="${CH_RELEASE_TAG:-cloud-hypervisor-fw_cfg}"

case "$(uname -m)" in
  x86_64)  SUFFIX="" ;;
  aarch64) SUFFIX="-aarch64" ;;
  *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;;
esac

BASE_URL="https://github.com/${CH_RELEASE_REPO}/releases/download/${CH_RELEASE_TAG}"

curl -fL "${BASE_URL}/cloud-hypervisor-static${SUFFIX}" -o cloud-hypervisor

sudo install -m 0755 cloud-hypervisor /usr/local/bin/cloud-hypervisor
rm -f cloud-hypervisor

sudo curl -fL "https://github.com/cloud-hypervisor/rust-hypervisor-firmware/releases/download/0.5.0/hypervisor-fw${SUFFIX}" -o "/usr/local/bin/hypervisor-fw"
sudo chmod 0755 /usr/local/bin/hypervisor-fw

/usr/local/bin/cloud-hypervisor --version
/usr/local/bin/cloud-hypervisor --help | grep -q fw-cfg-config \
  || echo "WARNING: cloud-hypervisor was not built with --features fw_cfg" >&2
