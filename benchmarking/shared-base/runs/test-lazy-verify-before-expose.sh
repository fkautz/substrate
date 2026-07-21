#!/usr/bin/env bash
# Test: Fetch, verify, and install content-addressed base blocks on first userfaultfd access, including zero-block and tamper paths.
# Why: This proves a lazy remote base can populate once per node while clones retain copy-on-write sharing and never expose bad content.
# Output: stdout. Configure LV_BASE_MB and LV_N through the environment; Linux arm64 userfaultfd is required by this prototype.
set -euo pipefail
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$script_dir/../../lazy_verify"
go run .
