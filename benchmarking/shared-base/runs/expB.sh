#!/bin/bash
H=/home/fkautz
R=$H/gvisor/bazel-bin/runsc/runsc_/runsc
B=$H/adkbundle
D=$H/expA/ckpt
ROOT="--root /run/expB --platform=kvm --ignore-cgroups --network=none"
> $H/resultB.txt
burst(){
  local N=$1
  pkill -9 -f "runsc-sandbox.*root=/run/expB" 2>/dev/null; sleep 3
  rm -f $H/bb*.log
  local t0=$(date +%s.%N)
  for i in $(seq 1 $N); do $R $ROOT restore --image-path=$D -bundle $B bb$i > $H/bb$i.log 2>&1 & done
  declare -A rt; local done=0
  while [ $done -lt $N ]; do
    for i in $(seq 1 $N); do [ -z "${rt[$i]}" ] && grep -q "tick=" $H/bb$i.log 2>/dev/null && { rt[$i]=$(date +%s.%N); done=$((done+1)); }; done
    sleep 0.05
    local now=$(date +%s.%N); (( $(echo "$now-$t0>150"|bc) )) && break
  done
  local tf="/tmp/t_$N"; : > $tf
  for i in $(seq 1 $N); do [ -n "${rt[$i]}" ] && echo "${rt[$i]} $t0" | awk "{printf \"%.2f\n\", \$1-\$2}" >> $tf; done
  sort -n $tf -o $tf
  local cnt=$(wc -l < $tf)
  local p50=$(sed -n "$(( (cnt+1)/2 ))p" $tf); local p99=$(sed -n "$(( (cnt*99+99)/100 ))p" $tf); local mx=$(tail -1 $tf)
  local ma=$(awk "/MemAvailable/{print int(\$2/1024)}" /proc/meminfo); local ld=$(cut -d" " -f1 /proc/loadavg)
  echo "N=$N: served=$cnt/$N  p50=${p50}s  p99=${p99}s  max=${mx}s   MemAvail=${ma}M  load=$ld" >> $H/resultB.txt
  for i in $(seq 1 $N); do $R $ROOT kill bb$i KILL 2>/dev/null; $R $ROOT delete --force bb$i 2>/dev/null; done
  sleep 3
}
burst 50
burst 100
echo "=== DONE ===" >> $H/resultB.txt
