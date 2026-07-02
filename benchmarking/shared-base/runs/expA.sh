#!/bin/bash
H=/home/fkautz
R=$H/gvisor/bazel-bin/runsc/runsc_/runsc
B=$H/adkbundle
D=$H/expA; rm -rf $D $H/seedA.log $H/warmA.log $H/cA*.log; mkdir -p $D/ckpt
pkill -9 -f runsc-sandbox 2>/dev/null; sleep 2
ROOT="--root /run/expA --platform=kvm --ignore-cgroups --network=none"
> $H/resultA.txt
$R $ROOT run -bundle $B seed > $H/seedA.log 2>&1 &
until grep -q "tick=" $H/seedA.log 2>/dev/null; do sleep 0.05; done
sleep 9
echo "=== COLD warm-up (seed): first request vs steady ===" >> $H/resultA.txt
echo "cold first : $(grep tick= $H/seedA.log | head -1)" >> $H/resultA.txt
echo "cold steady: $(grep tick= $H/seedA.log | tail -1)" >> $H/resultA.txt
lastseed=$(grep tick= $H/seedA.log | tail -1)
$R $ROOT checkpoint --shared-base --image-path=$D/ckpt seed >/dev/null 2>&1
$R $ROOT delete --force seed 2>/dev/null; pkill -9 -f "runsc-sandbox.*root=/run/expA" 2>/dev/null; sleep 2
{
echo ""
echo "=== #1 RESTORE warm-up: warm or thrash? ==="
echo "checkpoint was at: $lastseed"
} >> $H/resultA.txt
t0=$(date +%s.%N)
$R $ROOT restore --image-path=$D/ckpt -bundle $B warm > $H/warmA.log 2>&1 &
until grep -q "tick=" $H/warmA.log 2>/dev/null; do sleep 0.01; done
t1=$(date +%s.%N)
echo "restore->serving wall=$(echo "$t1-$t0"|bc)s" >> $H/resultA.txt
sleep 4
echo "first 5 post-restore ticks (dur_ms = warm-up cost):" >> $H/resultA.txt
grep tick= $H/warmA.log | head -5 >> $H/resultA.txt
echo "steady post-restore:" >> $H/resultA.txt
grep tick= $H/warmA.log | tail -2 >> $H/resultA.txt
{
echo ""
echo "=== #3 CORRECTNESS + STATE CONTINUITY ==="
echo "resp=BAD count post-restore: $(grep -c "resp=BAD" $H/warmA.log) (0 = all echoes verified)"
echo "continuity: checkpoint=[$lastseed]"
echo "        first restore=[$(grep tick= $H/warmA.log | head -1)]"
echo "(tick +1 and state_sum continues the running sum -> heap state survived)"
} >> $H/resultA.txt
$R $ROOT delete --force warm 2>/dev/null; pkill -9 -f "runsc-sandbox.*root=/run/expA" 2>/dev/null; sleep 2
echo "" >> $H/resultA.txt
echo "=== #3 CLONE INDEPENDENCE (3 from same checkpoint) ===" >> $H/resultA.txt
for i in 1 2 3; do $R $ROOT restore --image-path=$D/ckpt -bundle $B cA$i > $H/cA$i.log 2>&1 & done
sleep 6
for i in 1 2 3; do echo "clone$i last: $(grep tick= $H/cA$i.log | tail -1)" >> $H/resultA.txt; done
echo "(each diverges with its OWN tick/state_sum -> independent, no shared mutable state)" >> $H/resultA.txt
for i in 1 2 3; do $R $ROOT kill cA$i KILL 2>/dev/null; $R $ROOT delete --force cA$i 2>/dev/null; done
echo "=== DONE ===" >> $H/resultA.txt
