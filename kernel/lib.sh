# shellcheck shell=bash
# Shared by build.sh and update.sh, which source it.

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
fragment=$here/configs/c2vm.fragment

# For every make: run.sh's environment is arm64, like the kernel.
# shellcheck disable=SC2034 # used by build.sh and update.sh
make_args=(ARCH=arm64)

# series_of <version>: its series, e.g. 7.4 for 7.4 and 7.4.1.
series_of() {
	"$here/series.sh" "$1"
}

downloads=$here/downloads
# The checksum of VERSION's tarball: update.sh writes it once it has
# checked the release's signature, and build.sh checks the tarball
# against it.
# shellcheck disable=SC2034 # used by build.sh and update.sh
checksum=$here/SHA256

# kernel_url <version> <file>: where kernel.org has a release's file.
kernel_url() {
	echo "https://cdn.kernel.org/pub/linux/kernel/v${1%%.*}.x/$2"
}

# download <version>: downloads the release's tarball to downloads/,
# unless it's already there (e.g. a build and an update in the same CI
# job), and prints its name there.
download() {
	local tarball=linux-$1.tar.xz
	mkdir -p "$downloads"
	if [[ ! -f $downloads/$tarball ]]; then
		echo "downloading $tarball" >&2
		curl -fsSL --retry 3 -o "$downloads/$tarball.part" "$(kernel_url "$1" "$tarball")"
		mv "$downloads/$tarball.part" "$downloads/$tarball"
	fi
	echo "$tarball"
}

# verify <version> <dir>: checks the release's signature, with the keys
# in keys/, and prints its tarball's checksum, in sha256sum's format.
# The signature is over the uncompressed tarball: kernel.org compresses
# it after the upload. It goes in <dir>.
verify() {
	local version=$1 dir=$2
	local tarball keyrings=() key
	tarball=$(download "$version")
	curl -fsSL --retry 3 -o "$dir/linux-$version.tar.sign" \
		"$(kernel_url "$version" "linux-$version.tar.sign")"
	for key in "$here"/keys/*.asc; do
		keyrings+=(--keyring "$key")
	done
	# sqv reads stdin from /dev/stdin, not -.
	if ! xz -dc "$downloads/$tarball" |
		sqv "${keyrings[@]}" --signature-file "$dir/linux-$version.tar.sign" /dev/stdin > /dev/null; then
		rm -f "$downloads/$tarball"
		echo "couldn't verify linux-$version.tar.sign with kernel/keys/: removed $tarball." >&2
		echo "If it's signed with a key that isn't there, check that key and add it (see kernel/README.md)." >&2
		return 1
	fi
	(cd "$downloads" && sha256sum "$tarball")
}

# fetch <version> <dir> <checksum>: checks the release's tarball against
# <checksum>, in sha256sum's format, and extracts it in
# <dir>/linux-<version>.
fetch() {
	local version=$1 dir=$2 want=$3
	local tarball
	tarball=$(download "$version")
	if [[ $(cd "$downloads" && sha256sum "$tarball") != "$want" ]]; then
		rm -f "$downloads/$tarball"
		echo "$tarball doesn't match its checksum: removed it" >&2
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
