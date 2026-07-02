#!/usr/bin/env bash
# Reconstruct the buildable gVisor fork branch for the LLIFS shared-base density work.
#
# Produces a git branch `shared-base-density` = upstream gVisor at the pinned commit plus
# the single combined patch (gvisor-shared-base.patch). This is the canonical, order-free
# reconstruction. Do NOT apply the three incremental dev patches in the parent directory
# (gvisor3-s1.pgalloc.patch, c1-restore-plumbing.patch, c1b-checkpoint-plumbing.patch) in
# sequence: they were developed incrementally and c1b's diff overlaps c1 on sandbox.go, so
# a sequential apply conflicts. They are kept only as reviewable development history.
#
# Usage: ./make-gvisor-fork.sh [dest-dir]     (default: ./gvisor-shared-base)
# Then:  cd <dest> && bazel build //runsc:runsc
set -euo pipefail

GVISOR_COMMIT=928199eb9fb28593a69ac3daffdba01c3319dc8f
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
PATCH="$HERE/gvisor-shared-base.patch"
DEST=${1:-gvisor-shared-base}

[ -f "$PATCH" ] || { echo "missing $PATCH" >&2; exit 1; }
[ -e "$DEST" ] && { echo "$DEST already exists" >&2; exit 1; }

git clone https://github.com/google/gvisor.git "$DEST"
cd "$DEST"
git checkout -q "$GVISOR_COMMIT"
git checkout -q -b shared-base-density
git apply --index "$PATCH"
git -c user.name="${GIT_AUTHOR_NAME:-$(git config user.name 2>/dev/null || echo LLIFS)}" \
    -c user.email="${GIT_AUTHOR_EMAIL:-$(git config user.email 2>/dev/null || echo noreply@localhost)}" \
    commit -q -m "LLIFS GVISOR-3: shared copy-on-write base memory + delta-only checkpoint/restore

Base memory shared MAP_PRIVATE copy-on-write across restored sandboxes; SaveTo excludes
base-backed pages (delta-only checkpoint); LoadFrom overlays the base and applies the delta.
runsc: checkpoint --shared-base, and restore over a base.img. Base gVisor $GVISOR_COMMIT."

echo
echo "Ready: branch 'shared-base-density' in $DEST/  (gVisor $GVISOR_COMMIT + combined patch)."
echo "Build:  cd $DEST && bazel build --jobs=4 //runsc:runsc      # bazel 8.3.1 via bazelisk"
echo "Tests:  bazel test //pkg/sentry/pgalloc:pgalloc_test"
echo "Push (when ready): git remote add fork git@github.com:fkautz/gvisor.git && git push fork shared-base-density"
