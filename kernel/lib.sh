# shellcheck shell=bash
# Shared by build.sh and update.sh, which source it.

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
fragment=$here/configs/c2vm.fragment

# series_of <version>: its series, e.g. 7.4 for 7.4 and 7.4.1.
series_of() {
	"$here/series.sh" "$1"
}

# fetch <version> <dir>: downloads the release from kernel.org to
# downloads/ (unless it's already there, e.g. a build and an update in
# the same CI job), checks its checksum, and extracts it in
# <dir>/linux-<version>.
fetch() {
	local version=$1 dir=$2
	local tarball=linux-$version.tar.xz
	local base=https://cdn.kernel.org/pub/linux/kernel/v${version%%.*}.x
	local downloads=$here/downloads
	mkdir -p "$downloads"
	if [[ ! -f $downloads/$tarball ]]; then
		echo "downloading $tarball"
		curl -fsSL -o "$downloads/$tarball.part" "$base/$tarball"
		mv "$downloads/$tarball.part" "$downloads/$tarball"
	fi
	curl -fsSL -o "$dir/sha256sums.asc" "$base/sha256sums.asc"
	if ! (cd "$downloads" && awk -v f="$tarball" '$2 == f' "$dir/sha256sums.asc" | sha256sum -c -); then
		rm -f "$downloads/$tarball"
		echo "bad checksum for $tarball: removed it, try again" >&2
		return 1
	fi
	tar -xJf "$downloads/$tarball" -C "$dir"
}

# options <config>: the options set in a config, without the comments.
# That includes the ones the toolchain decides (its version, what it
# supports), and the ones that depend on them: the config is generated
# with run.sh's toolchain, which a build with another one doesn't match.
options() {
	grep -E '^(CONFIG_|# CONFIG_.* is not set$)' "$1"
}

# check_fragment <config>: fails, and prints them, if options the
# fragment sets aren't set that way in the config: its CONFIG_X=... and
# "# CONFIG_X is not set" lines, without trailing whitespace.
check_fragment() {
	local missing status=0
	missing=$(sed 's/[[:space:]]*$//' "$fragment" |
		grep -E '^(CONFIG_[A-Za-z0-9_]+=|# CONFIG_[A-Za-z0-9_]+ is not set$)' |
		grep -vxF -f "$1") || status=$?
	# grep exits with 1 when it selects nothing: here, nothing is missing.
	if [[ $status -gt 1 ]]; then
		echo "can't check $1 against c2vm.fragment" >&2
		return 1
	fi
	if [[ -n $missing ]]; then
		echo "not in $1 as c2vm.fragment sets them:" >&2
		echo "$missing" >&2
		return 1
	fi
}
