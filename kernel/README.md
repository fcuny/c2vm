# Guest kernel

The kernel c2vm boots with Apple's Virtualization.framework, for arm64.

- `VERSION`: the kernel release to build.
- `configs/aarch64.config`: Firecracker's CI guest config for 6.18, from Firecracker v1.17.0. It's a minimal config for microVMs, with everything built in, that also works with Virtualization.framework.
- `configs/c2vm.fragment`: what we add on top.
- `build.sh`: downloads the release from kernel.org, checks its checksum, applies the configs, and builds it.

The [kernel workflow](../.github/workflows/kernel.yml) builds it on each change and, on `main`, publishes it as `ghcr.io/fcuny/c2vm-kernel:<major.minor>` and `:<version>`. It's cross-compiled on x86_64, as arm64 runners aren't free for private repositories.

The config enables virtio over PCI, which Virtualization.framework uses, and builds everything in: there are no modules to ship.

To move to a new patch release, update `VERSION`. To move to a new series, also update the configs from the matching Firecracker release, if there is one.

## Testing a kernel

`test/` boots a kernel with a minimal Alpine root filesystem whose init reports what the kernel found, then reboots:

```sh
# Extract the kernel for the host's architecture, with crane...
crane export ghcr.io/fcuny/c2vm-kernel:6.18 - | tar -x boot/kernel
# ...or with podman.
podman create --name c2vm-kernel ghcr.io/fcuny/c2vm-kernel:6.18 none
podman export c2vm-kernel | tar -x boot/kernel
podman rm c2vm-kernel

# On macOS (needs `brew install vfkit e2fsprogs`):
kernel/test/make-rootfs.sh rootfs.ext4
kernel/test/boot-vz.sh boot/kernel rootfs.ext4
```

Each prints `PASS`, or the VM's full output if the test init didn't run.
