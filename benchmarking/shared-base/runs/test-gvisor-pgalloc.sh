#!/usr/bin/env bash
# Test: Run the patched gVisor pgalloc unit suite, including shared mapping and base/delta save/restore cases.
# Why: These are the lowest-level correctness tests for the external gVisor patches before runsc end-to-end experiments.
# Output: Bazel test output. First argument is the prepared gVisor checkout (default: $H/gvisor).
set -euo pipefail
H=${H:-/home/fkautz}
gvisor_dir=${1:-$H/gvisor}
cd "$gvisor_dir"
bazel test //pkg/sentry/pgalloc:pgalloc_test
