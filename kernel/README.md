# Guest kernel

The kernel c2vm boots with Apple's Virtualization.framework, for arm64, and a static `mke2fs` from [e2fsprogs](https://e2fsprogs.sourceforge.net/), which c2vm puts in the initramfs to format a disk-backed writable layer.

- `VERSION`: the kernel release to build.
- `SHA256`: the checksum of its tarball, which `update.sh` writes once it has checked the release's signature.
- `e2fsprogs/VERSION` and `e2fsprogs/SHA256`: the same for e2fsprogs, which `update-e2fsprogs.sh` writes.
- `keys/linux/` and `keys/e2fsprogs/`: the keys `update.sh` and `update-e2fsprogs.sh` check each project's releases with (see [Signatures](#signatures)).
- `configs/<series>/aarch64.config`: the full config for the series of `VERSION` (e.g. `7.2`), the one we ship. It started from Firecracker's CI guest config for 6.18 (see `configs/NOTICE`), a minimal config for microVMs that also works with Virtualization.framework.
- `configs/c2vm.fragment`: the options c2vm needs.
- `build.sh`: downloads the release from kernel.org, checks it against `SHA256`, and builds it with the series' config. It fails if `make olddefconfig` would change the config, toolchain options included, or if an option from the fragment isn't in it. It also builds `mke2fs` from the e2fsprogs release, checked against `e2fsprogs/SHA256`, linked statically. It fails if the binary isn't static, or if a filesystem it makes with its built-in defaults, as it will from the initramfs, fails Debian's `e2fsck` or lacks what overlayfs needs.
- `update-e2fsprogs.sh <version>`: moves `mke2fs` to a new e2fsprogs release. It checks the release's signature, and updates `e2fsprogs/SHA256` and `e2fsprogs/VERSION`. Nothing runs it on a schedule: e2fsprogs moves by hand.
- `update.sh <version> [<changes file>]`: moves to a new release. It checks the release's signature, updates `SHA256` and `VERSION`, and regenerates the config from the current one, merging in the fragment and giving new options their default value, then prints what changed in the config, and writes it to `<changes file>` if given (the kernel-update workflow puts it in its PR).
- `check-updates.sh`: runs `update.sh` and `update-e2fsprogs.sh` on the current releases, which should change nothing; the kernel workflow checks that they don't.
- `run.sh <command>`: runs `build.sh`, `update.sh`, `update-e2fsprogs.sh` or `check-updates.sh` with a pinned toolchain: Debian at a fixed digest, with the packages `builder.sh` installs from the snapshot of the archive that image was built from. On macOS it boots it with c2vm, with the checkout mounted (`KERNEL_VM_CPUS` and `KERNEL_VM_MEMORY` override the VM's size); elsewhere (and in CI), it runs it with docker, for arm64. The toolchain only changes when the digest does, and the config's toolchain options with it. The command's paths must be in the checkout.
- `bot.py`: what the workflows do around `update.sh`: decide whether there's a release to move to, and open the PRs. `bot_test.py` tests its decisions, and the kernel-bot workflow runs it.

```sh
make build  # on macOS, run.sh uses ./c2vm
kernel/run.sh kernel/update.sh 7.3.1
kernel/run.sh kernel/build.sh kernel/out
```

The [kernel workflow](../.github/workflows/kernel.yml) builds it on each change and, on `main`, publishes it as `ghcr.io/fcuny/c2vm-kernel:<major.minor>` and `:<version>`, with the kernel in `/boot/kernel`, its config in `/boot/config`, and `mke2fs` in `/sbin/mkfs.ext4`. It's built on an arm64 runner. The tags follow the kernel's version: a change to e2fsprogs, or to the config, publishes new contents under the same tags.

The config enables virtio over PCI, which Virtualization.framework uses, and builds everything in: there are no modules to ship.

We follow the latest stable series. The [kernel-update workflow](../.github/workflows/kernel-update.yml) checks kernel.org's latest stable release every day, and when it's newer than `VERSION`, runs `update.sh` with it and opens a draft PR, with the config changes in its description; it can also be run by hand with a release. Review the config changes, and test the kernel with `test/pr.py` before merging (see below). When the kernel workflow publishes a new series, it opens another draft PR, which moves the default kernel in `cmd/c2vm/kernel.go` (and the examples) to the new tag; run `make e2e` before merging it. Options c2vm needs go in `configs/c2vm.fragment`.

To skip a release, close its PR: the daily check won't open it again. Likewise, closing the PR for a new default kernel keeps the current one: it isn't opened again when the series gets new releases, and moving the default is then a PR by hand. Run by hand, the workflow goes ahead with any release other than the current one, e.g. to roll back, and fails when there's nothing to do. GitHub disables scheduled workflows after 60 days without activity in the repository: then there are no more PRs until it's enabled again, from the Actions tab.

## Signatures

Greg Kroah-Hartman signs every release on kernel.org, mainline ones included: `linux-<version>.tar.sign`, over the uncompressed tarball. `update.sh` checks it with `sqv`, against the keys in `keys/linux/`, and only then writes the tarball's checksum to `SHA256`, which `build.sh` checks. So builds don't depend on the keys, and a change to `SHA256` shows up in the update's PR. Each project has its own keys, so that one project's key can't sign the other's releases.

`keys/linux/gregkh.asc` is `647F 2865 4894 E3BD 4571  99BE 38DB BDC8 6092 693E`, as published in kernel.org's [pgpkeys repository](https://git.kernel.org/pub/scm/docs/kernel/pgpkeys.git/plain/keys/38DBBDC86092693E.asc), unchanged; [kernel.org](https://www.kernel.org/signature.html) lists the same fingerprint. If a release is signed with another key, `update.sh` fails, and the daily check opens no PR. Check the new key's fingerprint against at least two of kernel.org's sources (the pgpkeys repository, its Web Key Directory with `gpg --locate-keys <name>@kernel.org`, the release announcement), then add it to `keys/linux/` as `<name>.asc`.

We don't use `sha256sums.asc`: its signing key only shows that a file matches kernel.org's storage, and its binding signature uses SHA-1, which `sqv` rejects.

Theodore Ts'o signs e2fsprogs releases the same way: `e2fsprogs-<version>.tar.sign`, over the uncompressed tarball, which `update-e2fsprogs.sh` checks against `keys/e2fsprogs/`. `keys/e2fsprogs/tytso.asc` is `3AB0 57B7 E78D 945C 8C55  91FB D36F 769B C118 04F0`, as [keys.openpgp.org](https://keys.openpgp.org/vks/v1/by-fingerprint/3AB057B7E78D945C8C5591FBD36F769BC11804F0) publishes it, unchanged; kernel.org's pgpkeys repository and Debian's keyring list the same fingerprint. Releases are signed by its subkey `2B69 B954 DBFE 0879 2881  37C9 F2F9 5956 950D 81A3`.

It doesn't come from the pgpkeys repository, unlike Greg's: that copy, like Debian's, only keeps the subkey's latest binding signature, and `sqv` only accepts a release's signature if the subkey was bound when it was made. Ted renews the subkey's expiry with a new binding signature, so the latest one can postdate the release: 1.47.4 was signed on 2026-03-06, and the binding in the pgpkeys repository is from 2026-04-22. keys.openpgp.org keeps every binding signature. The subkey expires on 2027-10-14: a release signed after that needs a copy of the key with the renewed binding, from keys.openpgp.org again. Builds don't need the key, only `e2fsprogs/SHA256`.

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
