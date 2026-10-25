#!/usr/bin/env bash
#
# Runs a command in the kernel's build environment, from the top of the
# repository:
#
#   kernel/run.sh kernel/build.sh out
#   kernel/run.sh kernel/update.sh 7.3.1
#
# The environment is Debian at the digest below, with what builder.sh
# installs, so the toolchain only changes when they do. On macOS, it's a
# VM booted with c2vm (./c2vm, from `make build`, or c2vm on the PATH);
# elsewhere, a docker container. It's arm64, which build.sh builds for.

set -euo pipefail

image=docker.io/library/debian:trixie-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad

root=$(cd "$(dirname "$0")/.." && pwd)

if [[ $(uname -s) != Darwin ]]; then
	exec docker run --rm -v "$root:/src" "$image" /src/kernel/builder.sh "$@"
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
# (#57).
exec "$c2vm" boot -cpus "$(sysctl -n hw.ncpu)" -memory 8192 \
	-v "$root:/src" "$image" -- /src/kernel/builder.sh "$@"
