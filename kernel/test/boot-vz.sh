#!/usr/bin/env bash
#
# Boots a kernel with Apple's Virtualization.framework, through vfkit,
# and checks that the test init ran.
#
#   kernel/test/boot-vz.sh <kernel> <rootfs.ext4>
#
# Needs vfkit (brew install vfkit). The kernel must be an arm64 Image.

set -euo pipefail

kernel=${1:?usage: boot-vz.sh <kernel> <rootfs.ext4>}
rootfs=${2:?usage: boot-vz.sh <kernel> <rootfs.ext4>}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
log=$work/console.log

# vfkit insists on an initrd. An empty cpio archive is a valid, empty
# initramfs: with no /init in it, the kernel mounts root= as usual.
initrd=$work/empty.cpio
cpio -o -H newc </dev/null >"$initrd" 2>/dev/null

timeout=60
vfkit \
	--cpus 2 \
	--memory 512 \
	--bootloader "linux,kernel=$kernel,initrd=$initrd,cmdline=\"console=hvc0 root=/dev/vda rw init=/test-init boottest.halt=poweroff\"" \
	--device "virtio-blk,path=$rootfs" \
	--device virtio-net,nat \
	--device virtio-rng \
	--device "virtio-serial,logFilePath=$log" \
	>"$work/vfkit.log" 2>&1 &
pid=$!

for _ in $(seq "$timeout"); do
	kill -0 "$pid" 2>/dev/null || break
	sleep 1
done
if kill -0 "$pid" 2>/dev/null; then
	kill "$pid"
	echo "the VM didn't stop within ${timeout}s" >&2
fi
wait "$pid" || true

grep 'boot-test:' "$log" || true
if grep -q 'boot-test: OK' "$log"; then
	echo "PASS"
else
	echo "FAIL, vfkit output:" >&2
	cat "$work/vfkit.log" >&2
	echo "console:" >&2
	cat "$log" >&2 2>/dev/null || true
	exit 1
fi
