#!/usr/bin/env bash
set -Eeuo pipefail
export DEBIAN_FRONTEND=noninteractive


curl -L "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v51.1/cloud-hypervisor-static-aarch64" -o "cloud-hypervisor"
curl -L "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v51.1/ch-remote-static-aarch64" -o "ch-remote"

#curl -L "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v51.1/cloud-hypervisor-static" -o "cloud-hypervisor"

sudo mv cloud-hypervisor /usr/local/bin/
sudo mv ch-remote /usr/local/bin/

chmod a+x /usr/local/bin/cloud-hypervisor
chmod a+x /usr/local/bin/ch-remote


#sudo curl -L "https://github.com/cloud-hypervisor/rust-hypervisor-firmware/releases/download/0.5.0/hypervisor-fw" -o "/usr/local/bin/hypervisor-fw"
sudo curl -L "https://github.com/cloud-hypervisor/rust-hypervisor-firmware/releases/download/0.5.0/hypervisor-fw-aarch64" -o "/usr/local/bin/hypervisor-fw"
