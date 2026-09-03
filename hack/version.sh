#!/bin/sh
set -eu

unset CDPATH
root=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

fail() {
	echo "$*" >&2
	exit 1
}

tailscale_module_version=$(go list -m -f '{{.Version}}' tailscale.com)
printf '%s\n' "$tailscale_module_version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' ||
	fail "tailscale.com must be pinned to a stable release, got $tailscale_module_version"
tailscale_version=${tailscale_module_version#v}

rpp_revision=${RPPROXY_REVISION:-$(cat RPP_REVISION)}
printf '%s\n' "$rpp_revision" | grep -Eq '^[1-9][0-9]*$' ||
	fail "invalid tailrpproxy revision: $rpp_revision"

long_version="${tailscale_version}-rpp.${rpp_revision}"

case "${1:-long}" in
	long)
		printf '%s\n' "$long_version"
		;;
	tailscale)
		printf '%s\n' "$tailscale_version"
		;;
	revision)
		printf '%s\n' "$rpp_revision"
		;;
	release)
		tag=${2:-}
		[ "$tag" = "v${long_version}" ] ||
			fail "release tag must be v${long_version}, got ${tag:-<empty>}"
		previous=${RPPROXY_PREVIOUS_VERSION:-$(git describe --tags --match 'v[0-9]*.[0-9]*.[0-9]*-rpp.*' --abbrev=0 "${tag}^{commit}^" 2>/dev/null || true)}
		if [ -z "$previous" ]; then
			[ "$rpp_revision" = 1 ] || fail "the first rpp release must use revision 1"
			exit 0
		fi
		previous=${previous#v}
		previous_tailscale=${previous%-rpp.*}
		previous_revision=${previous##*-rpp.}
		printf '%s\n' "$previous_revision" | grep -Eq '^[1-9][0-9]*$' ||
			fail "invalid previous rpp release: v${previous}"
		if [ "$previous_tailscale" != "$tailscale_version" ]; then
			[ "$rpp_revision" = 1 ] ||
				fail "Tailscale changed from $previous_tailscale to $tailscale_version; rpp revision must reset to 1"
		elif [ "$rpp_revision" -le "$previous_revision" ]; then
			fail "rpp revision must be greater than $previous_revision for Tailscale $tailscale_version"
		fi
		;;
	ldflags)
		printf '%s\n' "-X tailscale.com/version.longStamp=${long_version} -X tailscale.com/version.shortStamp=${tailscale_version}"
		;;
	*)
		echo "usage: $0 [long|tailscale|revision|release TAG|ldflags]" >&2
		exit 2
		;;
esac
