#!/usr/bin/env bash
#
# Runs update.sh and update-e2fsprogs.sh on the current releases:
#
#   kernel/check-updates.sh
#
# Nothing else runs them until there's a new release. With the same
# toolchain, they check the releases' signatures again, and rewrite the
# same checksums and config, which the kernel workflow checks with git
# diff. They run together so that run.sh sets up the toolchain once.

set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)

"$here/update.sh" "$(cat "$here/VERSION")"
"$here/update-e2fsprogs.sh" "$(cat "$here/e2fsprogs/VERSION")"
