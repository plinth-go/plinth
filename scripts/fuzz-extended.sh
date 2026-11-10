#!/usr/bin/env bash
set -uo pipefail

go_bin="${GO:-go}"
duration="${1:-1m}"
status=0

mkdir -p fuzz-results
for target in FuzzStrictJSONBinding FuzzRouterPath; do
  log="fuzz-results/${target}.log"
  printf 'Running %s for %s\n' "$target" "$duration" | tee "$log"
  if ! "$go_bin" test ./kernel -run '^$' -fuzz "^${target}$" -fuzztime="$duration" -count=1 2>&1 | tee -a "$log"; then
    status=1
  fi
done

exit "$status"
