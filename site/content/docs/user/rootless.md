---
title: "Running kind with Rootless Docker"
menu:
  main:
    parent: "user"
    identifier: "rootless"
    weight: 3
---
Starting with kind 0.11.0 and Docker 20.10, Rootless Docker can be used as the node provider of kind.

Rootless Podman is not supported at the moment.

## Host requirements
### Kernel
The kernel needs to be 5.7 or later currently.
In future, we may be able to support a broader range of the kernel version.

### cgroup v2
The host needs to be running with cgroup v2.

cgroup v2 is enabled by default on Fedora.
On other distros, cgroup v2 can be typically enabled by adding `GRUB_CMDLINE_LINUX="systemd.unified_cgroup_hierarchy=1"` to `/etc/default/grub` and
running `sudo update-grub`.

Also, depending on the host configuration, the following steps might be needed:

- Create `/etc/systemd/system/user@.service.d/delegate.conf` with the following content, and then run `sudo systemctl daemon-reload`:
```ini
[Service]
Delegate=yes
```

- Create `/etc/sysctl.d/99-rootless.conf` with the following content, and then run `sudo sysctl --system`:
```
net.netfilter.nf_conntrack_max=131072
kernel.dmesg_restrict=0
```

## Restrictions

The restrictions of Rootless Docker apply to kind clusters as well.

e.g.
- OverlayFS cannot be used unless the host is using kernel >= 5.11, or Ubuntu/Debian kernel
- Cannot mount block storages
- Cannot mount NFS

## Creating a kind cluster with Rootless Docker

To create a kind cluster with Rootless Docker, just run:
```console
$ export DOCKER_HOST=unix://${XDG_RUNTIME_DIR}/docker.sock
$ kind create cluster
```
