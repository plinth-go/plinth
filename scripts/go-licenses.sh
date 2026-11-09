#!/usr/bin/env bash
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

GO=${GO:-go}
GO_LICENSES_VERSION=${GO_LICENSES_VERSION:-v1.6.0}
GOOS=${GOOS:-linux}
tmpdir=$(mktemp -d)
trap 'rm -r "$tmpdir"' EXIT

GOBIN="$tmpdir" "$GO" install "github.com/google/go-licenses@${GO_LICENSES_VERSION}"
GOOS="$GOOS" "$tmpdir/go-licenses" "$@"
