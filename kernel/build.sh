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
series=${version%.*}

config=$here/configs/$series/aarch64.config
if [[ ! -f $config ]]; then
	echo "no config for $series: expected $config" >&2
	exit 1
fi

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

cp "$config" "$src/.config"
make -C "$src" "${make_args[@]}" olddefconfig

# The config is the one we ship, so olddefconfig shouldn't change it. If
# it does, e.g. a patch release added an option, the config needs to be
# regenerated. Only the options, not the comments, are compared, and the
# ones the toolchain decides (its version, what it supports) are left out.
options() {
	grep -E '^(CONFIG_|# CONFIG_.* is not set$)' "$1" |
		grep -vE '^(# )?CONFIG_(CC|GCC|CLANG|AS|LD|LLD|RUSTC|PAHOLE|ARM64_AS)_'
}
if ! diff -u <(options "$config") <(options "$src/.config") >&2; then
	echo "olddefconfig changed $config" >&2
	exit 1
fi

# Kconfig only warns when an option can't be set, e.g. a dependency is
# missing. Fail instead for the ones c2vm needs.
missing=0
while IFS= read -r line; do
	if ! grep -qxF "$line" "$src/.config"; then
		echo "not in the config: $line" >&2
		missing=1
	fi
done < <(grep -E '^(CONFIG_|# CONFIG_)' "$here/configs/c2vm.fragment")
if [[ $missing -ne 0 ]]; then
	exit 1
fi

make -C "$src" "${make_args[@]}" -j"$(nproc)" Image

mkdir -p "$out/arm64"
cp "$src/arch/arm64/boot/Image" "$out/arm64/kernel"
cp "$src/.config" "$out/arm64/config"
echo "built $out/arm64/kernel ($version)"
