# Ignition

The following is a minimal Ignition configuration that contains everything cloud-hypervisor-provider-specific to get the service up and running.

```yaml
variant: fcos
version: 1.3.0
systemd:
  units:
    - name: cloud-hypervisor-prepare.service
      enabled: true
      contents: |
        [Unit]
        Description=Installation of cloud-hypervisor-provider needed components
        ConditionFirstBoot=yes
        After=systemd-networkd-wait-online.service 

        [Service]
        Type=oneshot
        ExecStart=/opt/cloud-hypervisor-prepare.sh

        [Install]
        WantedBy=multi-user.target
    - name: cloud-hypervisor@.service
      contents: |
        [Unit]
        Description=Cloud Hypervisor Instance %i
        After=network-online.target
        Wants=network-online.target

        [Service]
        Type=simple
        User=chp
        Group=chp

        RuntimeDirectory=chp/ch
        RuntimeDirectoryMode=0755

        ExecStart=/usr/local/bin/cloud-hypervisor --api-socket /run/chp/ch/%i.sock -v

        Restart=on-failure
        RestartSec=1

        StandardOutput=journal
        StandardError=journal

        [Install]
        WantedBy=multi-user.target
    - name: cloud-hypervisor.target
      enabled: true
      contents: |
        [Unit]
        Description=Cloud Hypervisor Fleet
        Wants=cloud-hypervisor@1.service
        Wants=cloud-hypervisor@2.service
        Wants=cloud-hypervisor@3.service
        Wants=cloud-hypervisor@4.service
        Wants=cloud-hypervisor@5.service
        Wants=cloud-hypervisor@6.service
        Wants=cloud-hypervisor@7.service
        Wants=cloud-hypervisor@8.service
        Wants=cloud-hypervisor@9.service
        Wants=cloud-hypervisor@10.service

        [Install]
        WantedBy=multi-user.target
storage:
  directories:
    - path: /var/lib/chp
      user:
        name: chp
      group:
        name: chp
      mode: 0755

  files:
    - path: /opt/cloud-hypervisor-prepare.sh
      mode: 0755
      overwrite: yes
      contents:
        inline: |
          #!/usr/bin/env bash
          set -Eeuo pipefail
          export DEBIAN_FRONTEND=noninteractive


          # curl -L "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v51.1/cloud-hypervisor-static-aarch64" -o "cloud-hypervisor"
          curl -L "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v51.1/cloud-hypervisor-static" -o "cloud-hypervisor"
          sudo mv cloud-hypervisor /usr/local/bin/
          chmod a+x /usr/local/bin/cloud-hypervisor


passwd:
  groups:
    - name: chp
      gid: 65532 # specific GID used in the IronCore context
  users:
    - name: chp
      uid: 65532 # specific UID used in the IronCore context
      primary_group: chp
      home_dir: "/nonexistent" # Ubuntu/Debian standard for system users without home directories
      no_create_home: true
      no_user_group: true
      shell: "/sbin/nologin"
```
