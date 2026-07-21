#!/usr/bin/env bash
# Test: Compile and run the standalone Linux MAP_PRIVATE copy-on-write density smoke test.
# Why: This proves the host-kernel sharing primitive independently of gVisor before testing runtime integration.
# Output: stdout.
set -euo pipefail
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
binary=$(mktemp "${TMPDIR:-/tmp}/density-smoke.XXXXXX")
trap 'rm -f "$binary"' EXIT
gcc -O2 -o "$binary" "$script_dir/../../density_smoke.c"
"$binary"
