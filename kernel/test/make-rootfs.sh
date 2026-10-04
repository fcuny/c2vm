#!/usr/bin/env bash
#
# Builds a minimal Alpine root filesystem whose init prints what the
# kernel found and reboots, to check that a kernel boots.
#
#   kernel/test/make-rootfs.sh <x86_64|aarch64> <output.ext4>
#
# Doesn't need root: mkfs.ext4 -d populates the filesystem from a
# directory. On macOS, install e2fsprogs with Homebrew.

set -euo pipefail

arch=${1:?usage: make-rootfs.sh <x86_64|aarch64> <output.ext4>}
out=${2:?usage: make-rootfs.sh <x86_64|aarch64> <output.ext4>}

mkfs=mkfs.ext4
if ! command -v "$mkfs" >/dev/null && command -v brew >/dev/null; then
	mkfs=$(brew --prefix e2fsprogs)/sbin/mkfs.ext4
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

base=https://dl-cdn.alpinelinux.org/alpine/latest-stable/releases/$arch
file=$(curl -fsSL "$base/latest-releases.yaml" | awk '/flavor: alpine-minirootfs/ { found = 1 } found && /file:/ { print $2; exit }')
sha=$(curl -fsSL "$base/latest-releases.yaml" | awk '/flavor: alpine-minirootfs/ { found = 1 } found && /sha256:/ { print $2; exit }')
echo "downloading $file"
curl -fsSL -o "$work/$file" "$base/$file"
if command -v sha256sum >/dev/null; then
	echo "$sha  $work/$file" | sha256sum -c -
else
	echo "$sha  $work/$file" | shasum -a 256 -c -
fi

mkdir "$work/root"
tar -xzf "$work/$file" -C "$work/root"

cat >"$work/root/test-init" <<'INIT'
#!/bin/sh
mount -t proc proc /proc
mount -t sysfs sysfs /sys
echo "boot-test: kernel $(uname -r) on $(uname -m), $(nproc) cpus"
echo "boot-test: cmdline: $(cat /proc/cmdline)"
for d in /sys/bus/virtio/devices/*; do
	echo "boot-test: virtio device $(cat "$d/device") ($(basename "$(readlink "$d/driver")" 2>/dev/null))"
done
echo "boot-test: OK"
# Virtualization.framework stops the VM when the guest powers off (on
# reboot, it restarts it). boot-vz.sh asks for that on the command line.
case " $(cat /proc/cmdline) " in
*" boottest.halt=poweroff "*) poweroff -f ;;
*) reboot -f ;;
esac
INIT
chmod 0755 "$work/root/test-init"

rm -f "$out"
"$mkfs" -q -L rootfs -d "$work/root" "$out" 64M
echo "built $out"
