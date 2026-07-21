#!/usr/bin/env bash
# Test: Restore three agents from one shared checkpoint and compare their independently advancing tick/state values.
# Why: The base must be shared read-only while each clone's mutable state remains isolated by copy-on-write.
# Output: $H/clone-independence.txt
H=${H:-/home/fkautz}; R=${RUNSC:-$H/gvisor/bazel-bin/runsc/runsc_/runsc}; B=${BUNDLE:-$H/adkbundle}
D=$H/clone-independence; ROOT="--root /run/clone-independence --platform=kvm --ignore-cgroups --network=none"
rm -rf "$D"; mkdir -p "$D/ckpt"
$R $ROOT run -bundle "$B" seed > "$D/seed.log" 2>&1 &
until grep -q tick= "$D/seed.log"; do sleep 0.05; done; sleep 9
$R $ROOT checkpoint --shared-base --image-path="$D/ckpt" seed >/dev/null 2>&1
$R $ROOT delete --force seed 2>/dev/null; sleep 2
for i in 1 2 3; do $R $ROOT restore --image-path="$D/ckpt" -bundle "$B" clone$i > "$D/clone$i.log" 2>&1 & done
sleep 6
: > "$H/clone-independence.txt"
for i in 1 2 3; do echo "clone$i last: $(grep tick= "$D/clone$i.log" | tail -1)" >> "$H/clone-independence.txt"; done
for i in 1 2 3; do $R $ROOT kill clone$i KILL 2>/dev/null; $R $ROOT delete --force clone$i 2>/dev/null; done
