#!/usr/bin/env bash
# Test: Create shared-base and conventional KVM checkpoints of the same warm ADK workload and compare time and on-disk components.
# Why: This quantifies the one-time shared base and per-agent delta/state cost that determines parked-agent density.
# Output: $H/checkpoint-storage.txt
H=${H:-/home/fkautz}; R=${RUNSC:-$H/gvisor/bazel-bin/runsc/runsc_/runsc}; B=${BUNDLE:-$H/adkbundle}
D=$H/checkpoint-storage; ROOT="--root /run/checkpoint-storage --platform=kvm --ignore-cgroups --network=none"
rm -rf "$D"; mkdir -p "$D/shared" "$D/full"
$R $ROOT run -bundle "$B" shared-seed > "$D/shared.log" 2>&1 &
until grep -q tick= "$D/shared.log"; do sleep 0.05; done; sleep 8
t0=$(date +%s.%N); $R $ROOT checkpoint --shared-base --image-path="$D/shared" shared-seed >/dev/null 2>&1; t1=$(date +%s.%N)
$R $ROOT delete --force shared-seed 2>/dev/null; sleep 2
$R $ROOT run -bundle "$B" full-seed > "$D/full.log" 2>&1 &
until grep -q tick= "$D/full.log"; do sleep 0.05; done; sleep 8
$R $ROOT checkpoint --image-path="$D/full" full-seed >/dev/null 2>&1
{
  echo "shared-base checkpoint wall=$(echo "$t1-$t0" | bc)s"
  echo "base once: $(du -h "$D/shared/base.img" | cut -f1)"
  echo "per agent: checkpoint=$(du -h "$D/shared/checkpoint.img" | cut -f1) delta=$(du -h "$D/shared/pages.img" | cut -f1) metadata=$(du -h "$D/shared/pages_meta.img" | cut -f1)"
  echo "normal: pages=$(du -h "$D/full/pages.img" | cut -f1) checkpoint=$(du -h "$D/full/checkpoint.img" | cut -f1)"
} > "$H/checkpoint-storage.txt"
$R $ROOT delete --force full-seed 2>/dev/null
