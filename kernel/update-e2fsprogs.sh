#!/usr/bin/env bash
#
# Moves the mke2fs shipped with the kernel to a new e2fsprogs release:
#
#   kernel/update-e2fsprogs.sh <version>
#
# Checks <version>'s signature with the keys in keys/e2fsprogs/, and
# writes its tarball's checksum to e2fsprogs/SHA256 and <version> to
# e2fsprogs/VERSION, which build.sh reads.
#
# Run it with run.sh, like build.sh.

set -euo pipefail

version=${1:?usage: update-e2fsprogs.sh <version>}

# shellcheck source-path=SCRIPTDIR source=lib.sh
source "$(dirname "$0")/lib.sh"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

sum=$(verify e2fsprogs "$version" "$work")
mkdir -p "$e2fsprogs"
echo "$sum" > "$e2fsprogs/SHA256"
echo "$version" > "$e2fsprogs/VERSION"
echo "e2fsprogs is now $version"
