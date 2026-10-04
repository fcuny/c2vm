#!/usr/bin/env bash
#
# Builds the guest kernel, for arm64: Virtualization.framework runs
# guests of the Mac's architecture.
#
#   kernel/build.sh <output dir>
#
# The kernel ends up in <output dir>/arm64/kernel, along with its config.
# Needs a Linux host with the usual kernel build dependencies, plus
# gcc-aarch64-linux-gnu to cross-compile when the host isn't arm64.

set -euo pipefail

out=${1:?usage: build.sh <output dir>}

here=$(cd "$(dirname "$0")" && pwd)
version=$(cat "$here/VERSION")
major=${version%%.*}

make_args=(ARCH=arm64)
if [[ $(uname -m) != aarch64 ]]; then
	make_args+=(CROSS_COMPILE=aarch64-linux-gnu-)
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

tarball=linux-$version.tar.xz
base=https://cdn.kernel.org/pub/linux/kernel/v$major.x
echo "downloading $tarball"
curl -fsSL -o "$work/$tarball" "$base/$tarball"
curl -fsSL -o "$work/sha256sums.asc" "$base/sha256sums.asc"
(cd "$work" && grep " $tarball\$" sha256sums.asc | sha256sum -c -)

tar -xJf "$work/$tarball" -C "$work"
src=$work/linux-$version

cp "$here/configs/aarch64.config" "$src/.config"
(cd "$src" && ./scripts/kconfig/merge_config.sh -m .config "$here/configs/c2vm.fragment")
make -C "$src" "${make_args[@]}" olddefconfig

# merge_config.sh and olddefconfig only warn when an option can't be
# set, e.g. a dependency is missing. Fail instead for the ones we need.
required=(
	VIRTIO_PCI      # Virtualization.framework's devices are on PCI
	VIRTIO_BLK      # the image
	VIRTIO_NET
	VIRTIO_CONSOLE  # hvc0
	VIRTIO_FS
	HW_RANDOM_VIRTIO
	BLK_DEV_INITRD  # c2vm-init runs from an initramfs...
	OVERLAY_FS      # ...and puts a writable layer over the image
	EXT4_FS
	TMPFS
	DEVTMPFS
	PROC_FS
	SYSFS
	UNIX98_PTYS
	IP_PNP          # ip=dhcp on the command line
	IP_PNP_DHCP
)
missing=0
for opt in "${required[@]}"; do
	if ! grep -qx "CONFIG_$opt=y" "$src/.config"; then
		echo "CONFIG_$opt is not built in" >&2
		missing=1
	fi
done
if [[ $missing -ne 0 ]]; then
	exit 1
fi

make -C "$src" "${make_args[@]}" -j"$(nproc)" Image

mkdir -p "$out/arm64"
cp "$src/arch/arm64/boot/Image" "$out/arm64/kernel"
cp "$src/.config" "$out/arm64/config"
echo "built $out/arm64/kernel ($version)"
