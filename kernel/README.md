# Guest kernel

The kernel c2vm boots with Apple's Virtualization.framework, for arm64.

- `VERSION`: the kernel release to build.
- `configs/aarch64.config`: Firecracker's CI guest config for 6.18, from Firecracker v1.17.0. It's a minimal config for microVMs, with everything built in, that also works with Virtualization.framework. `build.sh` brings it forward to newer kernels with `make olddefconfig`, which gives new options their default value.
- `configs/c2vm.fragment`: what we add on top.
- `build.sh`: downloads the release from kernel.org, checks its checksum, applies the configs, and builds it.

The [kernel workflow](../.github/workflows/kernel.yml) builds it on each change and, on `main`, publishes it as `ghcr.io/fcuny/c2vm-kernel:<major.minor>` and `:<version>`. It's built on an arm64 runner.

The config enables virtio over PCI, which Virtualization.framework uses, and builds everything in: there are no modules to ship.

We follow the latest stable series. To move to a new patch release, update `VERSION`. To move to a new series, also update the default kernel in `cmd/c2vm/kernel.go` (and the examples above) to the new tag. `build.sh` fails if an option c2vm needs isn't built in anymore; if a new series needs config changes, put them in `configs/c2vm.fragment`.

## Testing a kernel

`test/` boots a kernel with a minimal Alpine root filesystem whose init reports what the kernel found, then reboots:

```sh
# Extract the kernel for the host's architecture, with crane...
crane export ghcr.io/fcuny/c2vm-kernel:7.2 - | tar -x boot/kernel
# ...or with podman.
podman create --name c2vm-kernel ghcr.io/fcuny/c2vm-kernel:7.2 none
podman export c2vm-kernel | tar -x boot/kernel
podman rm c2vm-kernel

# On macOS (needs `brew install vfkit e2fsprogs`):
kernel/test/make-rootfs.sh rootfs.ext4
kernel/test/boot-vz.sh boot/kernel rootfs.ext4
```

Each prints `PASS`, or the VM's full output if the test init didn't run.
