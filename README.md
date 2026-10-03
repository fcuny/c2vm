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

You'll need a kernel to boot the VM. The following builds a 6.1 kernel with the configuration Firecracker uses in its CI:

```sh
git clone --depth 1 --branch v6.1 https://github.com/torvalds/linux.git linux.git
cd linux.git
curl -fsSL -o .config https://raw.githubusercontent.com/firecracker-microvm/firecracker/v1.17.0/resources/guest_configs/microvm-kernel-ci-x86_64-6.1.config
make olddefconfig
make vmlinux -j"$(nproc)"
```

### CNI

You need the CNI plugins (`bridge`, `host-local` and `firewall` from [containernetworking/plugins](https://github.com/containernetworking/plugins)) installed under `/opt/cni/bin`, along with [tc-redirect-tap](https://github.com/awslabs/tc-redirect-tap). The recommended configuration is stored under `hack/cni` and needs to be copied to `/etc/cni/conf.d`.

### Firecracker binaries

Running `make all` builds `c2vm`, downloads Firecracker under `hack/firecracker`, installs the CNI configuration and installs `tc-redirect-tap`.

### Running

```sh
sudo ./c2vm \
  -container docker.io/library/alpine:latest \
  -kernel linux.git/vmlinux \
  -firecracker-binary hack/firecracker/release-v1.17.0-x86_64/firecracker-v1.17.0-x86_64
```

containerd needs fully qualified image references (`docker.io/library/alpine:latest`, not `alpine`).
