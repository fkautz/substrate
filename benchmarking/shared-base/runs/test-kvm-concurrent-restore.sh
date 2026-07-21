#!/usr/bin/env bash
# Test: Restore eight KVM agents concurrently from a fresh shared-base checkpoint and report the slowest time-to-service.
# Why: This captures small-burst activation latency separately from single restore latency and scheduler-scale bursts.
# Output: $H/kvm-concurrent-restore.txt
H=${H:-/home/fkautz}; R=${RUNSC:-$H/gvisor/bazel-bin/runsc/runsc_/runsc}; B=${BUNDLE:-$H/adkbundle}
D=$H/kvm-concurrent; ROOT="--root /run/kvm-concurrent --platform=kvm --ignore-cgroups --network=none"
rm -rf "$D"; mkdir -p "$D/ckpt"
$R $ROOT run -bundle "$B" seed > "$D/seed.log" 2>&1 &
until grep -q tick= "$D/seed.log"; do sleep 0.05; done
$R $ROOT checkpoint --shared-base --image-path="$D/ckpt" seed >/dev/null 2>&1
$R $ROOT delete --force seed 2>/dev/null; sleep 2
t0=$(date +%s.%N); declare -A ready
for i in $(seq 1 8); do $R $ROOT restore --image-path="$D/ckpt" -bundle "$B" clone$i > "$D/clone$i.log" 2>&1 & done
done_count=0
while [ "$done_count" -lt 8 ]; do
  for i in $(seq 1 8); do
    [ -z "${ready[$i]}" ] && grep -q tick= "$D/clone$i.log" 2>/dev/null && { ready[$i]=$(date +%s.%N); done_count=$((done_count+1)); }
  done
  sleep 0.03
done
slowest=0
for i in $(seq 1 8); do elapsed=$(echo "${ready[$i]}-$t0" | bc); (( $(echo "$elapsed>$slowest" | bc) )) && slowest=$elapsed; done
echo "eight concurrent restores: slowest serving at ${slowest}s" > "$H/kvm-concurrent-restore.txt"
for i in $(seq 1 8); do $R $ROOT kill clone$i KILL 2>/dev/null; $R $ROOT delete --force clone$i 2>/dev/null; done
