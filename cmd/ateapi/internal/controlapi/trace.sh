#!/bin/sh
#
# Requirement traceability gate for the Control API (issue #307 follow-on).
# Reconciles spec/requirements.md against the `// Verifies:` tags in the package
# tests (and, once present, spec/classification.md + spec/waivers.md), then
# regenerates spec/traceability.md.
#
#   ./trace.sh           integrity check + regenerate the matrix (dev/CI gate)
#   ./trace.sh -strict   release gate: every requirement tested or waived
#
# classification.md / waivers.md are picked up automatically once they exist.
# Requires the `trace-check` binary
# (go install github.com/fkautz/trace-check/cmd/trace-check).

set -e
cd "$(dirname "$0")"

# Make the go-installed binary resolvable without the caller exporting PATH.
PATH="$PATH:${GOBIN:-$HOME/go/bin}"
export PATH

set -- -config tracecheck.json -root . -catalog spec/requirements.md -out spec/traceability.md "$@"
[ -f spec/classification.md ] && set -- -classification spec/classification.md "$@"
[ -f spec/waivers.md ] && set -- -waivers spec/waivers.md "$@"

exec trace-check "$@"
