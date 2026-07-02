#!/bin/bash
H=/home/fkautz
R=$H/gvisor/bazel-bin/runsc/runsc_/runsc; B=$H/c1test/bundle
pkill -f runsc-sandbox 2>/dev/null; sleep 1
ROOT="--root /run/gr --platform=kvm --ignore-cgroups --network=none"
$R $ROOT run -bundle $B gr > $H/gr.log 2>&1 & sleep 6
> $H/thr.txt
for pid in $(pgrep -f "runsc-sandbox.*root=/run/gr"); do
  echo "SENTRY: OS-threads=$(ls /proc/$pid/task 2>/dev/null | wc -l) VmRSS=$(awk "/VmRSS/{print int(\$2/1024)}" /proc/$pid/status)M VmData=$(awk "/VmData/{print int(\$2/1024)}" /proc/$pid/status)M VmStk=$(awk "/VmStk/{print \$2}" /proc/$pid/status)kB" >> $H/thr.txt
done
for pid in $(pgrep -f "gofer.*root=/run/gr"); do
  echo "GOFER:  OS-threads=$(ls /proc/$pid/task 2>/dev/null | wc -l) VmRSS=$(awk "/VmRSS/{print int(\$2/1024)}" /proc/$pid/status)M" >> $H/thr.txt
done
$R $ROOT kill gr KILL 2>/dev/null; $R $ROOT delete --force gr 2>/dev/null
echo "=== DONE ===" >> $H/thr.txt
