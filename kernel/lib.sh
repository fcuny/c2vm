# shellcheck shell=bash
# Shared by build.sh, update.sh and update-e2fsprogs.sh, which source it.

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
# against it. e2fsprogs/ has the same for e2fsprogs, which
# update-e2fsprogs.sh writes.
# shellcheck disable=SC2034 # used by build.sh and update.sh
checksum=$here/SHA256
# shellcheck disable=SC2034 # used by build.sh and update-e2fsprogs.sh
e2fsprogs=$here/e2fsprogs

# The functions below take a project, linux or e2fsprogs: both are
# released on kernel.org, as <project>-<version>.tar.xz, with a signature
# over the uncompressed tarball, <project>-<version>.tar.sign.

# url <project> <version> <file>: where kernel.org has a release's file.
url() {
	case $1 in
	linux) echo "https://cdn.kernel.org/pub/linux/kernel/v${2%%.*}.x/$3" ;;
	e2fsprogs) echo "https://cdn.kernel.org/pub/linux/kernel/people/tytso/e2fsprogs/v$2/$3" ;;
	*)
		echo "unknown project $1" >&2
		return 1
		;;
	esac
}

# download <project> <version>: downloads the release's tarball to
# downloads/, unless it's already there (e.g. a build and an update in
# the same CI job), and prints its name there.
#
# Here and in verify, failures return explicitly: callers run them in a
# command substitution, where set -e doesn't apply, and a failed
# download would otherwise show up as a bad signature or checksum.
download() {
	local tarball=$1-$2.tar.xz
	local from
	from=$(url "$1" "$2" "$tarball") || return
	mkdir -p "$downloads"
	if [[ ! -f $downloads/$tarball ]]; then
		echo "downloading $tarball" >&2
		curl -fsSL --retry 3 -o "$downloads/$tarball.part" "$from" || return
		mv "$downloads/$tarball.part" "$downloads/$tarball"
	fi
	echo "$tarball"
}

# verify <project> <version> <dir>: checks the release's signature, with
# the keys in keys/<project>/ only, and prints its tarball's checksum, in
# sha256sum's format. The signature goes in <dir>.
verify() {
	local project=$1 version=$2 dir=$3
	local tarball sign=$1-$2.tar.sign keyrings=() key
	# Before the download: without keys, sqv would fail as if the
	# release were signed with another key.
	for key in "$here/keys/$project"/*.asc; do
		if [[ -f $key ]]; then
			keyrings+=(--keyring "$key")
		fi
	done
	if [[ ${#keyrings[@]} -eq 0 ]]; then
		echo "no keys in kernel/keys/$project/" >&2
		return 1
	fi
	tarball=$(download "$project" "$version") || return
	local from
	from=$(url "$project" "$version" "$sign") || return
	curl -fsSL --retry 3 -o "$dir/$sign" "$from" || return
	# sqv reads stdin from /dev/stdin, not -.
	if ! xz -dc "$downloads/$tarball" |
		sqv "${keyrings[@]}" --signature-file "$dir/$sign" /dev/stdin > /dev/null; then
		rm -f "$downloads/$tarball"
		echo "couldn't verify $sign with kernel/keys/$project/: removed $tarball." >&2
		echo "If it's signed with a key that isn't there, check that key and add it (see kernel/README.md)." >&2
		return 1
	fi
	(cd "$downloads" && sha256sum "$tarball")
}

# pinned <file> <project> <version> <update script>: prints the checksum
# in <file>, which the update script writes, if it's for the release's
# tarball, and fails otherwise.
pinned() {
	local file=$1 project=$2 version=$3 update=$4
	local sum
	sum=$(cat "$file" 2> /dev/null) || true
	if [[ ${sum##* } != "$project-$version.tar.xz" ]]; then
		echo "${file#"$here"/} isn't for $project $version: run $update $version" >&2
		return 1
	fi
	echo "$sum"
}

# fetch <project> <version> <dir> <checksum>: checks the release's
# tarball against <checksum>, in sha256sum's format, and extracts it in
# <dir>/<project>-<version>.
fetch() {
	local project=$1 version=$2 dir=$3 want=$4
	local tarball
	tarball=$(download "$project" "$version")
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
