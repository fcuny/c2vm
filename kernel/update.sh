#!/usr/bin/env bash
#
# Moves the guest kernel to a new release:
#
#   kernel/update.sh <version>
#
# Writes <version> to VERSION, and regenerates the config for its series
# from the current one: c2vm.fragment is merged in, and olddefconfig
# gives new options their default value. A new series replaces the old
# one's config. The changes to the config are printed, to review them.
#
# Needs a Linux host, like build.sh.

set -euo pipefail

version=${1:?usage: update.sh <version>}

# shellcheck source-path=SCRIPTDIR source=lib.sh
source "$(dirname "$0")/lib.sh"

series=$(series_of "$version")
current=$(cat "$here/VERSION")
old=$here/configs/$(series_of "$current")/aarch64.config
new=$here/configs/$series/aarch64.config

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fetch "$version" "$work"
src=$work/linux-$version

cp "$old" "$src/.config"
(cd "$src" && ./scripts/kconfig/merge_config.sh -m .config "$fragment")
make -C "$src" "${make_args[@]}" olddefconfig
# Kconfig only warns when an option can't be set, e.g. a dependency is
# missing.
if ! check_fragment "$src/.config"; then
	echo "Kconfig couldn't set them for $version" >&2
	exit 1
fi

"$src/scripts/diffconfig" "$old" "$src/.config" > "$work/diff"
if [[ $new != "$old" ]]; then
	rm -r "$(dirname "$old")"
	mkdir -p "$(dirname "$new")"
fi
cp "$src/.config" "$new"
echo "$version" > "$here/VERSION"

echo "moved from $current to $version; config changes:"
cat "$work/diff"
