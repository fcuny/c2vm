#!/usr/bin/env bash
#
# Runs a command in the kernel's build environment, e.g. from the top of
# the repository:
#
#   kernel/run.sh kernel/build.sh kernel/out
#   kernel/run.sh kernel/update.sh 7.3.1
#
# The checkout is mounted on /src, and the command runs from the same
# directory in it, so the paths it gets must be in the checkout: relative
# ones, or absolute ones, which are rewritten.
#
# The environment is Debian at the digest below, with what builder.sh
# installs from the snapshot of the archive that image was built from, so
# the toolchain only changes when the digest does. On macOS, it's a VM
# booted with c2vm (./c2vm, from `make build`, or c2vm on the PATH);
# KERNEL_VM_CPUS and KERNEL_VM_MEMORY (in MiB) override its size.
# Elsewhere, it's a docker container. Either way it's arm64, which
# build.sh builds for: on other hosts, docker needs qemu to emulate it.

set -euo pipefail

image=docker.io/library/debian:trixie-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad

root=$(cd "$(dirname "$0")/.." && pwd -P)

cwd=$(pwd -P)
case $cwd/ in
"$root"/*) dir=/src${cwd#"$root"} ;;
*)
	echo "run it from inside $root, which is all the command sees" >&2
	exit 1
	;;
esac
args=()
for arg in "$@"; do
	case $arg in
	"$root" | "$root"/*) args+=("/src${arg#"$root"}") ;;
	/*)
		echo "$arg isn't in $root, which is all the command sees" >&2
		exit 1
		;;
	*) args+=("$arg") ;;
	esac
done

if [[ $(uname -s) != Darwin ]]; then
	exec docker run --rm --platform linux/arm64 -v "$root:/src" "$image" \
		/src/kernel/builder.sh "$dir" "${args[@]}"
fi

c2vm=$root/c2vm
if [[ ! -x $c2vm ]]; then
	if ! c2vm=$(command -v c2vm); then
		echo "c2vm not found: run make build" >&2
		exit 1
	fi
fi

# The kernel's sources and build stay in the VM, on its writable layer,
# which gets half of its memory: tar can't extract them on the mount
# (#57). They need about 3 GiB: 8 GiB is plenty, but leave the Mac some.
# One CPU per GiB, so that make's jobs fit in the other half.
host=$(($(sysctl -n hw.memsize) / 1024 / 1024))
memory=${KERNEL_VM_MEMORY:-$((host - 2048 < 8192 ? host - 2048 : 8192))}
ncpu=$(sysctl -n hw.ncpu)
cpus=${KERNEL_VM_CPUS:-$((ncpu < memory / 1024 ? ncpu : memory / 1024))}

exec "$c2vm" boot -cpus "$cpus" -memory "$memory" \
	-v "$root:/src" "$image" -- /src/kernel/builder.sh "$dir" "${args[@]}"
