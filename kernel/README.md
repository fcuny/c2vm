# Guest kernel

The kernel c2vm boots with Apple's Virtualization.framework, for arm64.

- `VERSION`: the kernel release to build.
- `configs/<series>/aarch64.config`: the full config for the series of `VERSION` (e.g. `7.2`), the one we ship. It started from Firecracker's CI guest config for 6.18 (see `configs/NOTICE`), a minimal config for microVMs that also works with Virtualization.framework.
- `configs/c2vm.fragment`: the options c2vm needs.
- `build.sh`: downloads the release from kernel.org, checks its checksum, and builds it with the series' config. It fails if `make olddefconfig` would change the config (other than for what the toolchain decides), or if an option from the fragment isn't in it.

The [kernel workflow](../.github/workflows/kernel.yml) builds it on each change and, on `main`, publishes it as `ghcr.io/fcuny/c2vm-kernel:<major.minor>` and `:<version>`. It's built on an arm64 runner.

The config enables virtio over PCI, which Virtualization.framework uses, and builds everything in: there are no modules to ship.

We follow the latest stable series. To move to a new patch release, update `VERSION`; if `build.sh` says the config changed, regenerate it. To move to a new series, add its config in `configs/<series>/` (and remove the old one), and once it's published, update the default kernel in `cmd/c2vm/kernel.go` (and the examples below) to the new tag. Options c2vm needs go in `configs/c2vm.fragment`.

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
