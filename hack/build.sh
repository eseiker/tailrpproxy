#!/bin/sh
set -eu

unset CDPATH
root=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

output=${OUTPUT:-dist/tailrpproxy}
ldflags=$(./hack/version.sh ldflags)
if [ "${RPPROXY_STRIP:-0}" = "1" ]; then
	ldflags="$ldflags -s -w"
fi

mkdir -p "$(dirname -- "$output")"
go build -trimpath -ldflags="$ldflags" -o "$output" ./cmd/tailrpproxy
