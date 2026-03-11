# Local Development Setup

This guide walks through setting up a local development environment using a [Lima](https://lima-vm.io/) VM
with nested virtualization enabled.

## Prerequisites

- [Lima](https://lima-vm.io/) v2.0.0+
- macOS with Apple Silicon (M3 or later for nested virtualization)
- Go (version matching `go.mod`)

## 1. Create the Dev VM

The dev VM definition is located at `dev-vm/chp-dev.yaml`. It provisions an Ubuntu VM with:

- A `chp` system user/group (UID/GID 65532) that runs cloud-hypervisor
- A `chp-dev` user (your dev user inside the VM)
- Two cloud-hypervisor instances (`vm1`, `vm2`) managed via systemd
- Nested virtualization enabled for KVM access

```bash
limactl create --name chp-dev dev-vm/chp-dev.yaml
limactl start chp-dev
```

## 2. Shell into the VM

```bash
limactl shell chp-dev
```

Your host home directory is mounted read-only at `~` inside the VM.

## 3. Clone / Access the Repository

Since the host home directory is mounted, you can access the repository directly.
For a writable copy:

```bash
cd ~
git clone https://github.com/ironcore-dev/cloud-hypervisor-provider.git
cd cloud-hypervisor-provider
```

## 4. Verify Cloud-Hypervisor is Running

The VM provisioning automatically starts two cloud-hypervisor instances via systemd:

```bash
# Check service status
systemctl status cloud-hypervisor@vm1
systemctl status cloud-hypervisor@vm2

# Verify sockets exist
ls -la /run/chp/ch/
```

You should see `vm1.sock` and `vm2.sock` owned by `chp:chp`.

## 5. Verify KVM Access

```bash
# Check KVM is available
ls -la /dev/kvm

# Your user should be in the chp group
id
# Expected: groups=... chp ...
```

## 6. Run the Provider

The provider needs to use `/var/lib/chp` as its root directory (shared between `chp-dev` and `chp`):

```bash
go run ./cmd/cloud-hypervisor-provider \
  --provider-root-dir /var/lib/chp \
  --cloud-hypervisor-sockets-path /run/chp/ch \
  --cloud-hypervisor-firmware-path /usr/local/bin/hypervisor-fw
```

## 7. Run Tests

### Unit Tests

Unit tests do not require cloud-hypervisor or KVM:

```bash
make test
```

### Integration Tests

Integration tests require the cloud-hypervisor instances to be running and accessible:

```bash
export CH_SOCKET_DIR=/run/chp/ch/
export CH_FIRMWARE_PATH=/usr/local/bin/hypervisor-fw
make integration-tests
```

## Architecture Overview

```
Host (macOS)
 └── Lima VM (chp-dev)
      ├── chp-dev user ── runs the provider process
      │     └── writes disk images to /var/lib/chp (0775, chp:chp)
      ├── chp user ────── runs cloud-hypervisor via systemd
      │     ├── /run/chp/ch/vm1.sock
      │     ├── /run/chp/ch/vm2.sock
      │     └── reads disk images from /var/lib/chp
      └── /dev/kvm ────── chp user has access via kvm group
```

## Troubleshooting

### "Failed to ping cloud-hypervisor socket"

The provider can't connect to the sockets. Check permissions:

```bash
ls -la /run/chp/ch/*.sock
```

Sockets need group read/write for `chp-dev`. Fix with `sudo chmod g+rw /run/chp/ch/*.sock`.

### "Cannot open disk path" / "Permission denied (os error 13)"

Cloud-hypervisor (`chp` user) can't read disk images created by the provider (`chp-dev` user).
Ensure:

1. The provider uses `--provider-root-dir /var/lib/chp` (not the home directory)
2. `/var/lib/chp` has permissions `0775` and is owned by `chp:chp`
3. `chp-dev` is in the `chp` group (`id` should show `chp` in groups)

### "Error manipulating firmware file" / "No such file or directory"

The firmware file is missing. Check that the prepare service ran successfully:

```bash
systemctl status cloud-hypervisor-prepare.service
ls -la /usr/local/bin/hypervisor-fw
```

If missing, re-run:

```bash
sudo systemctl start cloud-hypervisor-prepare.service
```

### VM Won't Start / "KVM not available"

Ensure nested virtualization is enabled (requires Apple M3+):

```bash
grep -c vmx /proc/cpuinfo 2>/dev/null || grep -c svm /proc/cpuinfo 2>/dev/null
```

If 0, nested virtualization is not available on your hardware.
