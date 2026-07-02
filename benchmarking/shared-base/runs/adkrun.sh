#!/bin/bash
H=/home/fkautz
R=$H/gvisor/bazel-bin/runsc/runsc_/runsc
pkill -f "runsc-sandbox.*root=/run/adk" 2>/dev/null; sleep 1
ROOT="--root /run/adk --platform=kvm --ignore-cgroups --network=none"
rm -f $H/adk1.log
$R $ROOT run -bundle $H/adkbundle adk1 > $H/adk1.log 2>&1 &
sleep 32
{
  echo "--- container stdout/stderr ---"
  grep -vE "token usage metadata" $H/adk1.log | head -25
  echo "--- runsc list ---"
  $R $ROOT list 2>&1 | tail -3
} > $H/adkrun.txt 2>&1
$R $ROOT kill adk1 KILL 2>/dev/null; $R $ROOT delete --force adk1 2>/dev/null
echo "=== DONE ===" >> $H/adkrun.txt
