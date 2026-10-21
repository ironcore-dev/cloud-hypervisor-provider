# Local Development Setup

This guide walks through setting up a local development environment using a [Lima](https://lima-vm.io/) VM
with nested virtualization enabled.

## Prerequisites

- [Lima](https://lima-vm.io/) v2.0.0+
- macOS with Apple Silicon (M3 or later for nested virtualization)

## 1. Start the Dev VM

The dev VM definition is located at `dev-vm/chp-dev.yaml`. It provisions an Ubuntu VM with:

- A `chp` system user/group (UID/GID 65532) for the QMP monitor and shared storage
- A `chp-dev` user (your dev user inside the VM) with access to `chp` and `kvm`
- Cloud Hypervisor instances started on demand by the provider

```bash
make dev-vm-up
```

Use `make dev-vm-down` to delete the instance. After changing the Lima
template, run `make dev-vm-down && make dev-vm-up` so the instance picks up the
new configuration.

## 2. Run the Provider

```bash
make dev-vm-provider
```

The provider uses `/var/lib/chp` as its root directory, shared between
`chp-dev` and `chp`. The target starts it inside the VM using the repository
mounted from the host and refreshes the `chp`/`kvm` group membership for the
process. It remains attached to the terminal; stop it with `Ctrl-C`.

### Access the provider socket from macOS

Lima forwards the provider's Unix socket to the host automatically. Once the
provider is running, use the `unix://` URI form required by gRPC:

```bash
irictl-machine --address "unix://$(pwd)/iri-machinebroker.sock" get machine
```

The `irictl-machine` client must be compatible with the provider. Alternatively,
use the client installed inside the VM:

```bash
limactl shell chp-dev -- irictl-machine \
  --address unix:///run/chp/iri-machinebroker.sock get machine
```

`make dev-vm-up` prints the resolved `unix://` address after starting the VM.
If the provider listens on a different address, update the `guestSocket` entry
in `dev-vm/chp-dev.yaml` and recreate the VM.

## 3. Shell into the VM (optional)

```bash
limactl shell chp-dev
```

The host home directory is mounted read-only at the same absolute path inside
the VM, while the guest user's `~` is a separate writable home directory. The
repository is therefore available without cloning it:

```bash
cd /Users/<host-user>/path/to/cloud-hypervisor-provider
```

## 4. Run Tests

### Unit Tests

Unit tests do not require cloud-hypervisor or KVM:

```bash
make test
```

### Integration Tests

From inside the VM, integration tests start Cloud Hypervisor on demand and
require KVM plus the prepared firmware:

```bash
export CH_FIRMWARE_PATH=/usr/local/bin/hypervisor-fw
make integration-tests
```

## Troubleshooting

### "Failed to ping cloud-hypervisor socket"

The provider cannot connect to a per-machine socket. Check the provider root
and the effective groups used by the provider:

```bash
sudo -iu chp-dev id
ls -la /var/lib/chp
```

Per-machine Cloud Hypervisor sockets are created by the provider below
`/var/lib/chp/machines` and are not pre-created by systemd.

### "Cannot open disk path" / "Permission denied (os error 13)"

The QMP service (`chp` user) and provider (`chp-dev` user) must be able to
access the provider data directory. Ensure:

1. The provider uses `--provider-root-dir /var/lib/chp` (not the home directory)
2. `/var/lib/chp` has permissions `0775` and is owned by `chp:chp`
3. `chp-dev` is in the `chp` group (`sudo -iu chp-dev id` should show `chp`)

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
ls -l /dev/kvm
```

If `/dev/kvm` is missing, nested virtualization is not available to the VM.
