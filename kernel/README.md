# Guest kernel

The kernel c2vm boots with Apple's Virtualization.framework, for arm64.

- `VERSION`: the kernel release to build.
- `SHA256`: the checksum of its tarball, which `update.sh` writes once it has checked the release's signature.
- `keys/`: the keys `update.sh` checks releases' signatures with (see [Signatures](#signatures)).
- `configs/<series>/aarch64.config`: the full config for the series of `VERSION` (e.g. `7.2`), the one we ship. It started from Firecracker's CI guest config for 6.18 (see `configs/NOTICE`), a minimal config for microVMs that also works with Virtualization.framework.
- `configs/c2vm.fragment`: the options c2vm needs.
- `build.sh`: downloads the release from kernel.org, checks it against `SHA256`, and builds it with the series' config. It fails if `make olddefconfig` would change the config, toolchain options included, or if an option from the fragment isn't in it.
- `update.sh <version> [<changes file>]`: moves to a new release. It checks the release's signature, updates `SHA256` and `VERSION`, and regenerates the config from the current one, merging in the fragment and giving new options their default value, then prints what changed in the config, and writes it to `<changes file>` if given (the kernel-update workflow puts it in its PR).
- `run.sh <command>`: runs `build.sh` or `update.sh` with a pinned toolchain: Debian at a fixed digest, with the packages `builder.sh` installs from the snapshot of the archive that image was built from. On macOS it boots it with c2vm, with the checkout mounted (`KERNEL_VM_CPUS` and `KERNEL_VM_MEMORY` override the VM's size); elsewhere (and in CI), it runs it with docker, for arm64. The toolchain only changes when the digest does, and the config's toolchain options with it. The command's paths must be in the checkout.
- `bot.py`: what the workflows do around `update.sh`: decide whether there's a release to move to, and open the PRs. `bot_test.py` tests its decisions, and the kernel-bot workflow runs it.

```sh
make build  # on macOS, run.sh uses ./c2vm
kernel/run.sh kernel/update.sh 7.3.1
kernel/run.sh kernel/build.sh kernel/out
```

The [kernel workflow](../.github/workflows/kernel.yml) builds it on each change and, on `main`, publishes it as `ghcr.io/fcuny/c2vm-kernel:<major.minor>` and `:<version>`. It's built on an arm64 runner.

The config enables virtio over PCI, which Virtualization.framework uses, and builds everything in: there are no modules to ship.

We follow the latest stable series. The [kernel-update workflow](../.github/workflows/kernel-update.yml) checks kernel.org's latest stable release every day, and when it's newer than `VERSION`, runs `update.sh` with it and opens a draft PR, with the config changes in its description; it can also be run by hand with a release. Review the config changes, and test the kernel with `test/pr.py` before merging (see below). When the kernel workflow publishes a new series, it opens another draft PR, which moves the default kernel in `cmd/c2vm/kernel.go` (and the examples) to the new tag; run `make e2e` before merging it. Options c2vm needs go in `configs/c2vm.fragment`.

To skip a release, close its PR: the daily check won't open it again. Likewise, closing the PR for a new default kernel keeps the current one: it isn't opened again when the series gets new releases, and moving the default is then a PR by hand. Run by hand, the workflow goes ahead with any release other than the current one, e.g. to roll back, and fails when there's nothing to do. GitHub disables scheduled workflows after 60 days without activity in the repository: then there are no more PRs until it's enabled again, from the Actions tab.

## Signatures

Greg Kroah-Hartman signs every release on kernel.org, mainline ones included: `linux-<version>.tar.sign`, over the uncompressed tarball. `update.sh` checks it with `sqv`, against the keys in `keys/`, and only then writes the tarball's checksum to `SHA256`, which `build.sh` checks. So builds don't depend on the keys, and a change to `SHA256` shows up in the update's PR.

`keys/gregkh.asc` is `647F 2865 4894 E3BD 4571  99BE 38DB BDC8 6092 693E`, as published in kernel.org's [pgpkeys repository](https://git.kernel.org/pub/scm/docs/kernel/pgpkeys.git/plain/keys/38DBBDC86092693E.asc), unchanged; [kernel.org](https://www.kernel.org/signature.html) lists the same fingerprint. If a release is signed with another key, `update.sh` fails, and the daily check opens no PR. Check the new key's fingerprint against at least two of kernel.org's sources (the pgpkeys repository, its Web Key Directory with `gpg --locate-keys <name>@kernel.org`, the release announcement), then add it to `keys/` as `<name>.asc`.

We don't use `sha256sums.asc`: its signing key only shows that a file matches kernel.org's storage, and its binding signature uses SHA-1, which `sqv` rejects.

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
