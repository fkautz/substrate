#!/bin/sh
#
# Requirement traceability gate for the ateredis store (prototype, issue #307).
# Reconciles spec/requirements.md against the `// Verifies:` tags in the package
# tests and the waivers in spec/waivers.md, then regenerates spec/traceability.md.
#
#   ./trace.sh           integrity check + regenerate the matrix (dev/CI gate)
#   ./trace.sh -strict   release gate: every requirement tested or waived, and
#                        every wire-observable requirement real-cluster tested
#
# Requires the `trace-check` binary
# (go install github.com/fkautz/trace-check/cmd/trace-check).

set -e
cd "$(dirname "$0")"

# Make the go-installed binary resolvable without the caller exporting PATH.
PATH="$PATH:${GOBIN:-$HOME/go/bin}"
export PATH

exec trace-check \
  -config tracecheck.json \
  -root . \
  -catalog spec/requirements.md \
  -classification spec/classification.md \
  -waivers spec/waivers.md \
  -out spec/traceability.md \
  "$@"
