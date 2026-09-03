#!/bin/sh
set -eu

unset CDPATH
root=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

tailscale_module_version=$(go list -m -f '{{.Version}}' tailscale.com)
if ! printf '%s\n' "$tailscale_module_version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
	echo "tailscale.com must be pinned to a stable release, got $tailscale_module_version" >&2
	exit 1
fi
tailscale_version=${tailscale_module_version#v}

rpp_version=${RPPROXY_VERSION:-$(cat VERSION)}
if ! printf '%s\n' "$rpp_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$'; then
	echo "invalid tailrpproxy version: $rpp_version" >&2
	exit 1
fi

long_version="${tailscale_version}-rpp-${rpp_version}"

case "${1:-long}" in
	long)
		printf '%s\n' "$long_version"
		;;
	tailscale)
		printf '%s\n' "$tailscale_version"
		;;
	rpp)
		printf '%s\n' "$rpp_version"
		;;
	ldflags)
		printf '%s\n' "-X tailscale.com/version.longStamp=${long_version} -X tailscale.com/version.shortStamp=${tailscale_version}"
		;;
	*)
		echo "usage: $0 [long|tailscale|rpp|ldflags]" >&2
		exit 2
		;;
esac
