#!/usr/bin/env bash
#
# Prepare the exact gVisor source tree used by the shared-base experiments.
#
# This script clones upstream gVisor, checks out the pinned upstream revision,
# verifies the published patch checksums, and applies the four-patch series in
# order. Keeping those operations here makes the external gVisor dependency
# reproducible without asking testers to reconstruct an unpublished fork.
#
# Usage:
#   ./prepare-gvisor.sh [destination]
#
# The destination must not already exist. It defaults to ./gvisor-shared-base.
# Building and testing require Linux; see REPRODUCE.md for the Bazel commands.

set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
patch_dir="$script_dir/patches/gvisor-shared-base-v1"
destination=${1:-"$PWD/gvisor-shared-base"}
upstream_revision=928199eb9

if [[ -e "$destination" ]]; then
  echo "error: destination already exists: $destination" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$patch_dir" && sha256sum --check SHA256SUMS)
elif command -v shasum >/dev/null 2>&1; then
  (cd "$patch_dir" && shasum -a 256 --check SHA256SUMS)
else
  echo "error: sha256sum or shasum is required" >&2
  exit 1
fi

git clone https://github.com/google/gvisor.git "$destination"
git -C "$destination" checkout --detach "$upstream_revision"
git -C "$destination" switch -c shared-base-v1
git -C "$destination" am \
  "$patch_dir/0001-pgalloc-support-copy-on-write-shared-base-mappings.patch" \
  "$patch_dir/0002-pgalloc-save-and-restore-memory-as-base-plus-delta.patch" \
  "$patch_dir/0003-runsc-restore-checkpoints-over-an-optional-shared-ba.patch" \
  "$patch_dir/0004-runsc-add-shared-base-checkpoint-creation.patch"

echo
echo "Prepared patched gVisor at: $destination"
echo "Next: cd '$destination' and run the build/test commands in REPRODUCE.md."
