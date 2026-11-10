#!/usr/bin/env bash
set -euo pipefail

go_bin="${GO:-go}"
count="${BENCH_COUNT:-5}"
benchtime="${BENCH_TIME:-1s}"
cpu="${BENCH_CPU:-1}"
out_dir="${BENCH_OUT_DIR:-bench-results}"

mkdir -p "$out_dir"

budget_log="$out_dir/allocation-budgets.txt"
bench_log="$out_dir/bench.txt"

printf 'Running allocation budgets\n'
"$go_bin" test ./kernel \
  -run '^(TestHTTPAllocationBudgets|TestHTTPMiddlewareAllocationBudget|TestHTTPBodyLimitAllocationBudget)$' \
  -count=1 -v | tee "$budget_log"

printf 'Running representative benchmarks: count=%s benchtime=%s cpu=%s\n' \
  "$count" "$benchtime" "$cpu"
"$go_bin" test ./kernel \
  -run '^$' \
  -bench '^BenchmarkHTTP/(typed-get|typed-get-static|typed-get-header|typed-json-post|not-found)$' \
  -benchmem -benchtime="$benchtime" -count="$count" -cpu="$cpu" -timeout=10m | tee "$bench_log"

printf 'Wrote %s and %s\n' "$bench_log" "$budget_log"
