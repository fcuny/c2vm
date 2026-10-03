#!/usr/bin/env bash
#
# Builds the guest kernel for one architecture.
#
#   kernel/build.sh <x86_64|aarch64> <output dir>
#
# The kernel ends up in <output dir>/<amd64|arm64>/kernel, along with
# its config. Needs a Linux host with the usual kernel build
# dependencies, plus gcc-aarch64-linux-gnu to build aarch64 on x86_64.

set -euo pipefail

arch=${1:?usage: build.sh <x86_64|aarch64> <output dir>}
out=${2:?usage: build.sh <x86_64|aarch64> <output dir>}

here=$(cd "$(dirname "$0")" && pwd)
version=$(cat "$here/VERSION")
major=${version%%.*}

case $arch in
x86_64)
	make_args=(ARCH=x86_64)
	target=vmlinux
	image=vmlinux
	goarch=amd64
	;;
aarch64)
	make_args=(ARCH=arm64)
	if [[ $(uname -m) != aarch64 ]]; then
		make_args+=(CROSS_COMPILE=aarch64-linux-gnu-)
	fi
	target=Image
	image=arch/arm64/boot/Image
	goarch=arm64
	;;
*)
	echo "unsupported architecture: $arch" >&2
	exit 1
	;;
esac

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

cp "$here/configs/$arch.config" "$src/.config"
(cd "$src" && ./scripts/kconfig/merge_config.sh -m .config "$here/configs/c2vm.fragment")
make -C "$src" "${make_args[@]}" olddefconfig

# merge_config.sh and olddefconfig only warn when an option can't be
# set, e.g. a dependency is missing. Fail instead for the ones we need.
required=(
	VIRTIO_MMIO     # firecracker
	VIRTIO_PCI      # Virtualization.framework
	VIRTIO_BLK
	VIRTIO_NET
	VIRTIO_CONSOLE  # hvc0 on Virtualization.framework
	VIRTIO_FS
	HW_RANDOM_VIRTIO
	EXT4_FS
	DEVTMPFS
	DEVTMPFS_MOUNT
	PROC_FS
	SYSFS
	TMPFS
	UNIX98_PTYS
	IP_PNP          # ip= on the command line, used for the network config
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

make -C "$src" "${make_args[@]}" -j"$(nproc)" "$target"

mkdir -p "$out/$goarch"
cp "$src/$image" "$out/$goarch/kernel"
cp "$src/.config" "$out/$goarch/config"
echo "built $out/$goarch/kernel ($version, $arch)"
