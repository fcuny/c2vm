#!/usr/bin/env bash
#
# Boots a kernel with firecracker, using the kernel arguments c2vm
# uses, and checks that the test init ran.
#
#   kernel/test/boot-firecracker.sh <firecracker> <kernel> <rootfs.ext4>
#
# Needs read/write access to /dev/kvm.

set -euo pipefail

firecracker=${1:?usage: boot-firecracker.sh <firecracker> <kernel> <rootfs.ext4>}
kernel=${2:?usage: boot-firecracker.sh <firecracker> <kernel> <rootfs.ext4>}
rootfs=${3:?usage: boot-firecracker.sh <firecracker> <kernel> <rootfs.ext4>}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Keep in sync with internal/vm.
boot_args="console=ttyS0 reboot=k panic=1 acpi=off pci=off i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd init=/test-init random.trust_cpu=on"

cat >"$work/config.json" <<JSON
{
  "boot-source": {
    "kernel_image_path": "$(realpath "$kernel")",
    "boot_args": "$boot_args"
  },
  "drives": [
    {
      "drive_id": "rootfs",
      "path_on_host": "$(realpath "$rootfs")",
      "is_root_device": true,
      "is_read_only": false
    }
  ],
  "machine-config": {
    "vcpu_count": 2,
    "mem_size_mib": 256
  }
}
JSON

timeout 60 "$firecracker" --no-api --config-file "$work/config.json" >"$work/log" 2>&1 || true

grep 'boot-test:' "$work/log" || true
if grep -q 'boot-test: OK' "$work/log"; then
	echo "PASS"
else
	echo "FAIL, full output:" >&2
	cat "$work/log" >&2
	exit 1
fi
