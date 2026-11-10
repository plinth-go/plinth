#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  printf 'usage: %s BASE.txt NEW.txt\n' "$0" >&2
  exit 2
fi

base="$1"
new="$2"
go_bin="${GO:-go}"
benchstat_version="${BENCHSTAT_VERSION:-v0.0.0-20261009192801-be2c69fb417e}"

for file in "$base" "$new"; do
  if [ ! -f "$file" ]; then
    printf 'benchmark result not found: %s\n' "$file" >&2
    exit 1
  fi
done

"$go_bin" run "golang.org/x/perf/cmd/benchstat@${benchstat_version}" "$base" "$new"
