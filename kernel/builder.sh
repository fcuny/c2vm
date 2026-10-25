#!/usr/bin/env bash
#
# Installs the kernel's build dependencies, and runs a command from the
# top of the repository. run.sh runs it as root, in Debian, with the
# repository in /src.

set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
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

cd /src
# Run as the owner of the checkout, so what the command writes isn't
# root's.
exec setpriv --reuid="$(stat -c %u .)" --regid="$(stat -c %g .)" --clear-groups -- "$@"
