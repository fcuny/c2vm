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

# shellcheck source-path=SCRIPTDIR source=lib.sh
source "$(dirname "$0")/lib.sh"

version=$(cat "$here/VERSION")
series=$(series_of "$version")

config=$here/configs/$series/aarch64.config
if [[ ! -f $config ]]; then
	echo "no config for $series: run kernel/update.sh $version" >&2
	exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fetch "$version" "$work"
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
check_fragment "$src/.config"

make -C "$src" "${make_args[@]}" -j"$(nproc)" Image

mkdir -p "$out/arm64"
cp "$src/arch/arm64/boot/Image" "$out/arm64/kernel"
cp "$src/.config" "$out/arm64/config"
echo "built $out/arm64/kernel ($version)"
