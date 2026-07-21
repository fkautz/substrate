#!/usr/bin/env bash
# Test: Populate cold 2 MiB blocks through userfaultfd and report per-block latency and throughput.
# Why: This isolates the mechanism floor for lazy verified-base loading before adding content-hash cost.
# Output: stdout. Optional first argument sets total MiB (default 512); Linux userfaultfd is required.
set -euo pipefail
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
binary=$(mktemp "${TMPDIR:-/tmp}/uffd-latency.XXXXXX")
trap 'rm -f "$binary"' EXIT
gcc -O2 -pthread -o "$binary" "$script_dir/../../uffd_latency.c"
"$binary" "${1:-512}"
