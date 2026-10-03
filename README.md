# containerd-to-vm

## What

A recent [article](https://fly.io/blog/docker-without-docker/) from the team at [fly.io](https://fly.io) described how they build VMs for firecracker from the docker image provided by their customers. They outline the following steps:

1. Pull the matching container from the registry.
2. Create a loop device to store the container's filesystem on.
3. Unpack the container (in this case, using Docker's Go libraries) into the mounted loop device.
4. Create a second block device and inject our init, kernel, configuration, and other goop into.
5. Track down any persistent volumes attached to the application, unlock them with LUKS, and collect their unlocked block devices.
6. Create a TAP device, configure it for our network, and attach BPF code to it.
7. Hand all this stuff off to Firecracker and tell it to boot .

As I've been interested in playing with both containerd's API and firecracker, I thought it would be a good opportunity to try to implement this.

## How

You'll need a Linux host with KVM, a running containerd, and root (the tool mounts a loop device). You'll also need a few things before you can run it.

### Kernel

You'll need a kernel to boot the VM. A 6.18 kernel, built from the configuration Firecracker uses in its CI, is published for amd64 and arm64 as `ghcr.io/fcuny/c2vm-kernel:6.18`, with the kernel in `/boot/kernel`. To extract it:

```sh
crane export ghcr.io/fcuny/c2vm-kernel:6.18 - | tar -x boot/kernel
```

Or with podman (the image has no command, so `create` needs a placeholder; the container is never started):

```sh
podman create --name c2vm-kernel ghcr.io/fcuny/c2vm-kernel:6.18 none
podman export c2vm-kernel | tar -x boot/kernel
podman rm c2vm-kernel
```

Both pick the kernel for the host's architecture; pass `--platform linux/arm64` (or `linux/amd64`) to get the other one.

To build it yourself instead, on Linux, run `kernel/build.sh x86_64 out` (or `aarch64`); see [`kernel/`](kernel/).

### CNI

You need the CNI plugins (`bridge`, `host-local` and `firewall` from [containernetworking/plugins](https://github.com/containernetworking/plugins)) installed under `/opt/cni/bin`, along with [tc-redirect-tap](https://github.com/awslabs/tc-redirect-tap). The recommended configuration is stored under `hack/cni` and needs to be copied to `/etc/cni/conf.d`.

The VM's `/etc/resolv.conf` uses the nameservers from the CNI result, which `host-local` reads from the host's `/etc/resolv.conf`. If the host runs systemd-resolved, that file points at `127.0.0.53`, which the VM can't reach: set `resolvConf` in the configuration to `/run/systemd/resolve/resolv.conf` instead.

### Firecracker binaries

Running `make all` builds `c2vm` and `c2vm-init`, downloads Firecracker under `hack/firecracker`, installs the CNI configuration and installs `tc-redirect-tap`.

### Running

`c2vm` installs `c2vm-init` in the image as the VM's init. It runs the image's entrypoint and command as the image's user, from its working directory and with its environment, then shuts the VM down when the command exits. `c2vm` looks for `c2vm-init` next to itself; use `-init` to point elsewhere. It must be built for the same architecture as the image.

```sh
sudo ./c2vm \
  -container docker.io/library/alpine:latest \
  -kernel boot/kernel \
  -firecracker-binary hack/firecracker/release-v1.17.0-x86_64/firecracker-v1.17.0-x86_64
```

containerd needs fully qualified image references (`docker.io/library/alpine:latest`, not `alpine`).

Run `./c2vm -h` for the other options: the VM's CPUs and memory, the image's platform, the containerd and firecracker sockets, and the CNI network.
