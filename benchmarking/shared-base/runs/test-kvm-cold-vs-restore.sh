#!/usr/bin/env bash
# Test: Compare one ADK cold start with one shared-base restore on KVM, measuring invocation-to-first-service wall time.
# Why: This isolates the single-agent activation-latency improvement without burst concurrency effects.
# Output: $H/kvm-cold-vs-restore.txt
H=${H:-/home/fkautz}; R=${RUNSC:-$H/gvisor/bazel-bin/runsc/runsc_/runsc}; B=${BUNDLE:-$H/adkbundle}
D=$H/kvm-latency; ROOT="--root /run/kvm-latency --platform=kvm --ignore-cgroups --network=none"
rm -rf "$D"; mkdir -p "$D/ckpt"
t0=$(date +%s.%N); $R $ROOT run -bundle "$B" cold > "$D/cold.log" 2>&1 &
until grep -q tick= "$D/cold.log"; do sleep 0.05; done; t1=$(date +%s.%N); cold_wall=$(echo "$t1-$t0" | bc)
$R $ROOT checkpoint --shared-base --image-path="$D/ckpt" cold >/dev/null 2>&1
$R $ROOT delete --force cold 2>/dev/null; sleep 2
t0=$(date +%s.%N); $R $ROOT restore --image-path="$D/ckpt" -bundle "$B" restored > "$D/restored.log" 2>&1 &
until grep -q tick= "$D/restored.log"; do sleep 0.02; done; t1=$(date +%s.%N)
{
  echo "cold run-to-serving: ${cold_wall}s"
  echo "restore-to-serving: $(echo "$t1-$t0" | bc)s"
  echo "base=$(du -h "$D/ckpt/base.img" | cut -f1) delta=$(du -h "$D/ckpt/pages.img" | cut -f1)"
} > "$H/kvm-cold-vs-restore.txt"
$R $ROOT delete --force restored 2>/dev/null
