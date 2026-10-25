# shellcheck shell=bash
# Shared by build.sh and update.sh, which source it.

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
fragment=$here/configs/c2vm.fragment

# Builds for arm64, cross-compiling when the host isn't.
make_args=(ARCH=arm64)
if [[ $(uname -m) != aarch64 ]]; then
	make_args+=(CROSS_COMPILE=aarch64-linux-gnu-)
fi

# series_of <version>: its series, e.g. 7.4 for 7.4 and 7.4.1.
series_of() {
	cut -d. -f1-2 <<< "$1"
}

# fetch <version> <dir>: downloads the release from kernel.org, checks
# its checksum, and extracts it in <dir>/linux-<version>.
fetch() {
	local version=$1 dir=$2
	local tarball=linux-$version.tar.xz
	local base=https://cdn.kernel.org/pub/linux/kernel/v${version%%.*}.x
	echo "downloading $tarball"
	curl -fsSL -o "$dir/$tarball" "$base/$tarball"
	curl -fsSL -o "$dir/sha256sums.asc" "$base/sha256sums.asc"
	(cd "$dir" && grep " $tarball\$" sha256sums.asc | sha256sum -c -)
	tar -xJf "$dir/$tarball" -C "$dir"
}

# options <config>: the options set in a config, without the comments
# and the options the toolchain decides (its version, what it supports).
options() {
	grep -E '^(CONFIG_|# CONFIG_.* is not set$)' "$1" |
		grep -vE '^(# )?CONFIG_(CC|GCC|CLANG|AS|LD|LLD|RUSTC|PAHOLE|ARM64_AS)_'
}

# check_fragment <config>: fails, and prints them, if options the
# fragment sets aren't set that way in the config: its CONFIG_X=... and
# "# CONFIG_X is not set" lines, without trailing whitespace.
check_fragment() {
	local missing
	missing=$(sed 's/[[:space:]]*$//' "$fragment" |
		grep -E '^(CONFIG_[A-Za-z0-9_]+=|# CONFIG_[A-Za-z0-9_]+ is not set$)' |
		grep -vxF -f "$1") || true
	if [[ -n $missing ]]; then
		echo "not in $1 as c2vm.fragment sets them:" >&2
		echo "$missing" >&2
		return 1
	fi
}
