#!/usr/bin/env bash
#
# Installs the kernel's build dependencies, and runs a command:
#
#   builder.sh <directory> <command> [args...]
#
# run.sh runs it as root, in Debian, with the repository in /src, and
# the directory it was run from as <directory>.

set -euo pipefail

dir=${1:?usage: builder.sh <directory> <command> [args...]}
shift

# The image's sources name the snapshot of the archive it was built from,
# in a comment. Install from that snapshot, so the image's digest pins the
# packages too, not only the base. Over http: there are no CA
# certificates yet, and apt checks the signed Release files anyway.
# Snapshots' Release files expire, which doesn't matter here.
sources=/etc/apt/sources.list.d/debian.sources
if ! grep -q '^# http://snapshot.debian.org/' "$sources"; then
	echo "no snapshot in $sources" >&2
	exit 1
fi
sed -i -e '/^URIs: /d' -e 's|^# \(http://snapshot\.debian\.org/.*\)$|URIs: \1|' "$sources"

export DEBIAN_FRONTEND=noninteractive
apt-get -o Acquire::Check-Valid-Until=false update -qq
apt-get install -qq -y --no-install-recommends \
	bc \
	bison \
	ca-certificates \
	curl \
	flex \
	gcc \
	libc6-dev \
	libelf-dev \
	libssl-dev \
	make \
	perl \
	python3 \
	xz-utils \
	> /dev/null

cd "$dir"
# Run as the owner of the checkout, so what the command writes isn't
# root's.
exec setpriv --reuid="$(stat -c %u /src)" --regid="$(stat -c %g /src)" --clear-groups -- "$@"
