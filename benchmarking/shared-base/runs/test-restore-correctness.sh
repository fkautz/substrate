#!/usr/bin/env bash
# Test: Checkpoint a running ADK agent, restore it, and verify response integrity plus tick/state-sum continuity.
# Why: Memory sharing is useful only if restored application state remains bit-correct and resumes at the exact logical point.
# Output: $H/restore-correctness.txt
H=${H:-/home/fkautz}; R=${RUNSC:-$H/gvisor/bazel-bin/runsc/runsc_/runsc}; B=${BUNDLE:-$H/adkbundle}
D=$H/restore-correctness; ROOT="--root /run/restore-correctness --platform=kvm --ignore-cgroups --network=none"
rm -rf "$D"; mkdir -p "$D/ckpt"
$R $ROOT run -bundle "$B" seed > "$D/seed.log" 2>&1 &
until grep -q tick= "$D/seed.log"; do sleep 0.05; done; sleep 9
before=$(grep tick= "$D/seed.log" | tail -1)
$R $ROOT checkpoint --shared-base --image-path="$D/ckpt" seed >/dev/null 2>&1
$R $ROOT delete --force seed 2>/dev/null; sleep 2
$R $ROOT restore --image-path="$D/ckpt" -bundle "$B" restored > "$D/restored.log" 2>&1 &
until grep -q tick= "$D/restored.log"; do sleep 0.02; done; sleep 3
{
  echo "resp=BAD count: $(grep -c resp=BAD "$D/restored.log")"
  echo "checkpoint: $before"
  echo "first restored: $(grep tick= "$D/restored.log" | head -1)"
} > "$H/restore-correctness.txt"
$R $ROOT delete --force restored 2>/dev/null
