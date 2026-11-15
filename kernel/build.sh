#!/usr/bin/env bash
#
# Builds the guest kernel, for arm64: Virtualization.framework runs
# guests of the Mac's architecture, and a static mke2fs, which c2vm puts
# in the initramfs to format a disk-backed writable layer.
#
#   kernel/build.sh <output dir>
#
# <output dir>/arm64 is the image's root: the kernel ends up in
# boot/kernel, along with its config in boot/config, and mke2fs in
# sbin/mkfs.ext4. Run it with run.sh: the config is generated with its
# toolchain, and the build fails with another one.

set -euo pipefail

out=${1:?usage: build.sh <output dir>}

# shellcheck source-path=SCRIPTDIR source=lib.sh
source "$(dirname "$0")/lib.sh"

version=$(cat "$here/VERSION")
series=$(series_of "$version")

config=$here/configs/$series/aarch64.config
if [[ ! -f $config ]]; then
	echo "no config for $series: run kernel/update.sh $version" >&2
	exit 1
fi
# olddefconfig must leave the config as it is (see below), so checking it
# now is enough, and fails before the download.
if ! check_fragment "$config"; then
	echo "run kernel/update.sh $version to merge the fragment in" >&2
	exit 1
fi
sum=$(pinned "$checksum" linux "$version" kernel/update.sh)
e2fs_version=$(cat "$e2fsprogs/VERSION")
e2fs_sum=$(pinned "$e2fsprogs/SHA256" e2fsprogs "$e2fs_version" kernel/update-e2fsprogs.sh)

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

mkdir -p "$out/arm64/boot" "$out/arm64/sbin"

# mke2fs first: it builds in seconds, so it fails long before the
# kernel would. It runs from the initramfs, which has nothing else: it's
# linked statically, and uses its built-in defaults, as there's no
# /etc/mke2fs.conf. The toolchain doesn't install util-linux's libuuid
# and libblkid, so e2fsprogs builds its own.
fetch e2fsprogs "$e2fs_version" "$work" "$e2fs_sum"
e2fs_src=$work/e2fsprogs-$e2fs_version
(
	cd "$e2fs_src"
	./configure --quiet LDFLAGS=-static \
		--disable-nls --disable-fuse2fs --without-libarchive \
		--enable-libuuid --enable-libblkid
	make --quiet -j"$(nproc)" libs
	make --quiet -C misc mke2fs
)
mkfs=$out/arm64/sbin/mkfs.ext4
cp "$e2fs_src/misc/mke2fs" "$mkfs"
strip "$mkfs"
# Not a pipeline into grep -q: readelf could die of SIGPIPE once grep
# has matched, and pipefail would then take the binary for static.
headers=$(readelf -lW "$mkfs")
if [[ $headers == *'program interpreter'* ]]; then
	echo "$mkfs isn't static" >&2
	exit 1
fi

# Format a sparse file with it, as from the initramfs: no environment,
# and its built-in defaults. The toolchain's e2fsprogs installs
# /etc/mke2fs.conf, so MKE2FS_CONFIG names a file that doesn't exist,
# which makes mke2fs fall back to them. The toolchain's e2fsck, from
# Debian's own build, checks the result, and overlayfs needs the upper
# layer to have xattrs (ext_attr) and file types in directory entries
# (filetype).
scratch=$work/scratch.img
truncate -s 1G "$scratch"
env -i MKE2FS_CONFIG=/nonexistent "$mkfs" -F -q -t ext4 "$scratch"
if ! e2fsck -fn "$scratch" > /dev/null; then
	echo "e2fsck found errors in the filesystem $mkfs made" >&2
	exit 1
fi
features=$(dumpe2fs -h "$scratch" 2> /dev/null | sed -n 's/^Filesystem features: *//p')
for feature in ext_attr filetype extent; do
	if [[ " $features " != *" $feature "* ]]; then
		echo "$mkfs made a filesystem without $feature: $features" >&2
		exit 1
	fi
done
rm "$scratch"
echo "built $mkfs (e2fsprogs $e2fs_version, $(stat -c %s "$mkfs") bytes)"

fetch linux "$version" "$work" "$sum"
src=$work/linux-$version

cp "$config" "$src/.config"
make -C "$src" "${make_args[@]}" olddefconfig

# The config is the one we ship, so olddefconfig shouldn't change it. If
# it does, e.g. a patch release added an option, it needs to be
# regenerated.
if ! diff -u <(options "$config") <(options "$src/.config") >&2; then
	echo "olddefconfig changed $config: run kernel/update.sh $version" >&2
	exit 1
fi

make -C "$src" "${make_args[@]}" -j"$(nproc)" Image

cp "$src/arch/arm64/boot/Image" "$out/arm64/boot/kernel"
cp "$src/.config" "$out/arm64/boot/config"
echo "built $out/arm64/boot/kernel ($version)"
