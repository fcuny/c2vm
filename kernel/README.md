# Guest kernel

The kernel c2vm boots with Apple's Virtualization.framework, for arm64.

- `VERSION`: the kernel release to build.
- `configs/<series>/aarch64.config`: the full config for the series of `VERSION` (e.g. `7.2`), the one we ship. It started from Firecracker's CI guest config for 6.18 (see `configs/NOTICE`), a minimal config for microVMs that also works with Virtualization.framework.
- `configs/c2vm.fragment`: the options c2vm needs.
- `build.sh`: downloads the release from kernel.org, checks its checksum, and builds it with the series' config. It fails if `make olddefconfig` would change the config, toolchain options included, or if an option from the fragment isn't in it.
- `update.sh <version> [<changes file>]`: moves to a new release. It updates `VERSION` and regenerates the config from the current one, merging in the fragment and giving new options their default value, then prints what changed in the config, and writes it to `<changes file>` if given (the kernel-update workflow puts it in its PR).
- `run.sh <command>`: runs `build.sh` or `update.sh` with a pinned toolchain: Debian at a fixed digest, with the packages `builder.sh` installs from the snapshot of the archive that image was built from. On macOS it boots it with c2vm, with the checkout mounted (`KERNEL_VM_CPUS` and `KERNEL_VM_MEMORY` override the VM's size); elsewhere (and in CI), it runs it with docker, for arm64. The toolchain only changes when the digest does, and the config's toolchain options with it. The command's paths must be in the checkout.
- `bot.py`: what the workflows do around `update.sh`: decide whether there's a release to move to, and open the PRs. `bot_test.py` tests its decisions (`python3 kernel/bot_test.py`), and the kernel workflow runs it.

```sh
make build  # on macOS, run.sh uses ./c2vm
kernel/run.sh kernel/update.sh 7.3.1
kernel/run.sh kernel/build.sh kernel/out
```

The [kernel workflow](../.github/workflows/kernel.yml) builds it on each change and, on `main`, publishes it as `ghcr.io/fcuny/c2vm-kernel:<major.minor>` and `:<version>`. It's built on an arm64 runner.

The config enables virtio over PCI, which Virtualization.framework uses, and builds everything in: there are no modules to ship.

We follow the latest stable series. The [kernel-update workflow](../.github/workflows/kernel-update.yml) checks kernel.org's latest stable release every day, and when it's newer than `VERSION`, runs `update.sh` with it and opens a draft PR, with the config changes in its description; it can also be run by hand with a release. Review the config changes, and test the kernel with `test/pr.py` before merging (see below). When the kernel workflow publishes a new series, it opens another draft PR, which moves the default kernel in `cmd/c2vm/kernel.go` (and the examples) to the new tag; run `make e2e` before merging it. Options c2vm needs go in `configs/c2vm.fragment`.

To skip a release, close its PR: the daily check won't open it again. Run by hand, the workflow goes ahead with any release other than the current one, e.g. to roll back, and fails when there's nothing to do. GitHub disables scheduled workflows after 60 days without activity in the repository: then there are no more PRs until it's enabled again, from the Actions tab.

## Testing a kernel

`test/pr.py <PR>` tests the kernel a PR builds: it downloads the kernel workflow's artifact for the PR's head, boots it with c2vm, built from the checkout, to check what c2vm needs (the network, the overlay, `-v` mounts, the CPUs, the random device, dmesg), and runs `make e2e` with it. It prints a report, and asks before posting it on the PR. It runs with [uv](https://docs.astral.sh/uv/), on macOS on Apple silicon:

```sh
kernel/test/pr.py 66
```

Without c2vm, `make-rootfs.sh` and `boot-vz.sh` boot a kernel with vfkit and a minimal Alpine root filesystem whose init reports what the kernel found, then reboots:

```sh
# Extract the kernel for the host's architecture, with crane...
crane export ghcr.io/fcuny/c2vm-kernel:7.3 - | tar -x boot/kernel
# ...or with podman.
podman create --name c2vm-kernel ghcr.io/fcuny/c2vm-kernel:7.3 none
podman export c2vm-kernel | tar -x boot/kernel
podman rm c2vm-kernel

# On macOS (needs `brew install vfkit e2fsprogs`):
kernel/test/make-rootfs.sh rootfs.ext4
kernel/test/boot-vz.sh boot/kernel rootfs.ext4
```

Each prints `PASS`, or the VM's full output if the test init didn't run.
