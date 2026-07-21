#!/usr/bin/env bash
# Test: Compare ADK cold-start and conventional restore time-to-service on systrap, including response correctness.
# Why: This verifies the latency benefit is checkpoint/restore behavior rather than a KVM-only artifact; /dev/kvm is not required.
# Output: $H/systrap-cold-vs-restore.txt
H=${H:-/home/fkautz}; R=${RUNSC:-$H/gvisor/bazel-bin/runsc/runsc_/runsc}; B=${BUNDLE:-$H/adkbundle}
D=$H/systrap-latency; ROOT="--root /run/systrap-latency --platform=systrap --ignore-cgroups --network=none"
rm -rf "$D"; mkdir -p "$D/ckpt"
t0=$(date +%s.%N); $R $ROOT run -bundle "$B" cold > "$D/cold.log" 2>&1 &
until grep -q tick= "$D/cold.log"; do sleep 0.05; done; t1=$(date +%s.%N); cold_wall=$(echo "$t1-$t0" | bc)
$R $ROOT checkpoint --image-path="$D/ckpt" cold >/dev/null 2>&1
$R $ROOT delete --force cold 2>/dev/null; sleep 2
t0=$(date +%s.%N); $R $ROOT restore --image-path="$D/ckpt" -bundle "$B" restored > "$D/restored.log" 2>&1 &
until grep -q tick= "$D/restored.log"; do sleep 0.02; done; t1=$(date +%s.%N)
{
  echo "cold run-to-serving: ${cold_wall}s"
  echo "restore-to-serving: $(echo "$t1-$t0" | bc)s"
  echo "restored resp=BAD count: $(grep -c resp=BAD "$D/restored.log")"
} > "$H/systrap-cold-vs-restore.txt"
$R $ROOT delete --force restored 2>/dev/null
