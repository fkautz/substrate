#!/usr/bin/env bash
# Test: Verify a Terrapin-addressed base before exposure, MAP_PRIVATE-share it across clones, then demonstrate tamper rejection.
# Why: This proves integrity gating composes with copy-on-write density without a per-clone verification cost.
# Output: stdout. Configure VS_BASE_MB, VS_N, and VS_DELTA_MB through the environment.
set -euo pipefail
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$script_dir/../../verify_share"
go run .
