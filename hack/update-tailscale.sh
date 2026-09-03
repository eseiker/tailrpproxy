#!/bin/sh
set -eu

unset CDPATH
root=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

current=$(go list -m -f '{{.Version}}' tailscale.com)
current_wireguard=$(go list -m -f '{{.Version}}' github.com/tailscale/wireguard-go)
latest=$(go list -m -f '{{.Version}}' tailscale.com@latest)

if ! printf '%s\n' "$latest" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
	echo "refusing non-stable tailscale.com@latest version: $latest" >&2
	exit 1
fi

go mod download "tailscale.com@$latest"
latest_mod="$(go env GOMODCACHE)/cache/download/tailscale.com/@v/${latest}.mod"
wireguard_version=$(
	go mod edit -json "$latest_mod" |
		awk '/"Path": "github.com\/tailscale\/wireguard-go"/ { found=1; next } found && /"Version":/ { gsub(/[",]/, "", $2); print $2; exit }'
)
if [ -z "$wireguard_version" ]; then
	echo "tailscale.com $latest does not declare github.com/tailscale/wireguard-go" >&2
	exit 1
fi

if [ "$current" = "$latest" ] && [ "$current_wireguard" = "$wireguard_version" ]; then
	echo "Tailscale dependencies are already aligned with $latest"
	exit 0
fi

echo "updating tailscale.com from $current to $latest"
echo "aligning wireguard-go from $current_wireguard to $wireguard_version"
go get "tailscale.com@$latest" "github.com/tailscale/wireguard-go@$wireguard_version"
go mod tidy

resolved=$(go list -m -f '{{.Version}}' tailscale.com)
resolved_wireguard=$(go list -m -f '{{.Version}}' github.com/tailscale/wireguard-go)
if [ "$resolved" != "$latest" ]; then
	echo "tailscale.com resolved to $resolved, expected $latest" >&2
	exit 1
fi
if [ "$resolved_wireguard" != "$wireguard_version" ]; then
	echo "wireguard-go resolved to $resolved_wireguard, expected $wireguard_version" >&2
	exit 1
fi
if [ "$current" != "$latest" ]; then
	printf '1\n' >RPP_REVISION
	echo "reset rpp revision to 1 for $latest"
fi
