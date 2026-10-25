# Guest kernel

The kernel c2vm boots with Apple's Virtualization.framework, for arm64.

- `VERSION`: the kernel release to build.
- `configs/<series>/aarch64.config`: the full config for the series of `VERSION` (e.g. `7.2`), the one we ship. It started from Firecracker's CI guest config for 6.18 (see `configs/NOTICE`), a minimal config for microVMs that also works with Virtualization.framework.
- `configs/c2vm.fragment`: the options c2vm needs.
- `build.sh`: downloads the release from kernel.org, checks its checksum, and builds it with the series' config. It fails if `make olddefconfig` would change the config, toolchain options included, or if an option from the fragment isn't in it.
- `update.sh <version> [<changes file>]`: moves to a new release. It updates `VERSION` and regenerates the config from the current one, merging in the fragment and giving new options their default value, then prints what changed in the config, and writes it to `<changes file>` if given (the kernel-update workflow puts it in its PR).
- `run.sh <command>`: runs `build.sh` or `update.sh` with a pinned toolchain: Debian at a fixed digest, with the packages `builder.sh` installs from the snapshot of the archive that image was built from. On macOS it boots it with c2vm, with the checkout mounted (`KERNEL_VM_CPUS` and `KERNEL_VM_MEMORY` override the VM's size); elsewhere (and in CI), it runs it with docker, for arm64. The toolchain only changes when the digest does, and the config's toolchain options with it. The command's paths must be in the checkout.

```sh
make build  # on macOS, run.sh uses ./c2vm
kernel/run.sh kernel/update.sh 7.3.1
kernel/run.sh kernel/build.sh kernel/out
```

The [kernel workflow](../.github/workflows/kernel.yml) builds it on each change and, on `main`, publishes it as `ghcr.io/fcuny/c2vm-kernel:<major.minor>` and `:<version>`. It's built on an arm64 runner.

The config enables virtio over PCI, which Virtualization.framework uses, and builds everything in: there are no modules to ship.

We follow the latest stable series. To move to a new release, run the [kernel-update workflow](../.github/workflows/kernel-update.yml) with it: it runs `update.sh` and opens a draft PR, with the config changes in its description. Review them, and boot the kernel from the PR's `kernel` artifact before merging. For a new series, once its kernel is published, also update the default kernel in `cmd/c2vm/kernel.go` (and the examples below) to the new tag. Options c2vm needs go in `configs/c2vm.fragment`.

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
