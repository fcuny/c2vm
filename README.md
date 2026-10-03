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

It has since moved away from containerd: c2vm pulls images straight from registries and converts them to an ext4 image without mounting anything, so building an image needs neither a daemon nor root.

## How

You'll need a Linux host with KVM, and root to set up the VM's network with CNI. You'll also need a few things before you can run it.

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

`c2vm save` pulls an image and converts it to an ext4 image, which it caches by digest and prints the path of:

```sh
./c2vm save nginx:stable-alpine3.24-perl
```

`c2vm boot` does the same, then boots it, with the VM's console on the terminal:

```sh
sudo ./c2vm boot \
  -kernel boot/kernel \
  -firecracker-binary hack/firecracker/release-v1.17.0-x86_64/firecracker-v1.17.0-x86_64 \
  nginx:stable-alpine3.24-perl
```

Booting only works on Linux, with firecracker, for now. `save` works on macOS too.

Images are pulled from their registry, with short names resolved as docker does (`nginx` is `docker.io/library/nginx:latest`). Credentials come from your docker or podman login; public images are pulled anonymously when there are none, or when they can't be looked up.

Images are cached in `~/.cache/c2vm` on Linux and `~/Library/Caches/c2vm` on macOS (`-cache-dir` to change it), so booting an image again only checks which digest its tag points to. The cached image is a read-only ext4 filesystem. At boot, `c2vm-init` runs from an initramfs: it mounts the image with a writable in-memory layer on top, and runs the image's entrypoint and command as the image's user, from its working directory and with its environment. Changes are lost when the VM stops.

`c2vm` looks for `c2vm-init` next to itself; use `-init` to point elsewhere. It must be built for the same architecture as the image.

Run `./c2vm <command> -h` for the other options: the VM's CPUs and memory, the image's platform, the firecracker socket, and the CNI network.
