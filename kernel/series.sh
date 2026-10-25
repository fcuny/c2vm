#!/usr/bin/env bash
#
# Prints the series of a kernel release, e.g. 7.4 for 7.4 and 7.4.1:
#
#   kernel/series.sh [<version>]
#
# Without a release, the one in VERSION. The workflows use it for the
# image's tag, and lib.sh for the config's directory.

set -euo pipefail

version=${1:-$(cat "$(dirname "$0")/VERSION")}
cut -d. -f1-2 <<< "$version"
