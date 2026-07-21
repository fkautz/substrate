#!/usr/bin/env bash
# Test: Warm an ADK agent, checkpoint it, restore it, and compare the first five restored request durations with steady state.
# Why: This determines whether restore preserves a warm application or merely moves initialization work after restore.
# Output: $H/restore-warmth.txt
H=${H:-/home/fkautz}; R=${RUNSC:-$H/gvisor/bazel-bin/runsc/runsc_/runsc}; B=${BUNDLE:-$H/adkbundle}
D=$H/restore-warmth; ROOT="--root /run/restore-warmth --platform=kvm --ignore-cgroups --network=none"
rm -rf "$D"; mkdir -p "$D/ckpt"; : > "$H/restore-warmth.txt"
$R $ROOT run -bundle "$B" seed > "$D/seed.log" 2>&1 &
until grep -q tick= "$D/seed.log"; do sleep 0.05; done; sleep 9
last_seed=$(grep tick= "$D/seed.log" | tail -1)
$R $ROOT checkpoint --shared-base --image-path="$D/ckpt" seed >/dev/null 2>&1
$R $ROOT delete --force seed 2>/dev/null; sleep 2
t0=$(date +%s.%N); $R $ROOT restore --image-path="$D/ckpt" -bundle "$B" warm > "$D/warm.log" 2>&1 &
until grep -q tick= "$D/warm.log"; do sleep 0.01; done; t1=$(date +%s.%N); sleep 4
{
  echo "checkpoint was at: $last_seed"
  echo "restore-to-serving wall=$(echo "$t1-$t0" | bc)s"
  echo "first five restored ticks:"; grep tick= "$D/warm.log" | head -5
  echo "steady restored ticks:"; grep tick= "$D/warm.log" | tail -2
} >> "$H/restore-warmth.txt"
$R $ROOT delete --force warm 2>/dev/null
