#!/bin/bash
H=/home/fkautz
R=$H/gvisor/bazel-bin/runsc/runsc_/runsc
B=$H/adkbundle
CLK=$(getconf CLK_TCK)
> $H/resultC.txt
pkill -9 -f runsc-sandbox 2>/dev/null; sleep 2

### PART 1: snapshot storage + checkpoint time (KVM) ###
D=$H/expC; rm -rf $D; mkdir -p $D/sb $D/full
KROOT="--root /run/expC --platform=kvm --ignore-cgroups --network=none"
$R $KROOT run -bundle $B seed > $H/seedC.log 2>&1 &
until grep -q "tick=" $H/seedC.log 2>/dev/null; do sleep 0.05; done
sleep 8
{
echo "=== #1 SNAPSHOT STORAGE + CHECKPOINT TIME (KVM) ==="
echo "agent warm: $(grep tick= $H/seedC.log | tail -1)"
} >> $H/resultC.txt
t0=$(date +%s.%N)
$R $KROOT checkpoint --shared-base --image-path=$D/sb seed >/dev/null 2>&1
t1=$(date +%s.%N)
{
echo "checkpoint --shared-base wall=$(echo "$t1-$t0"|bc)s"
echo "  base.img (SHARED once): $(du -h $D/sb/base.img|cut -f1) actual / $(du -h --apparent-size $D/sb/base.img|cut -f1) apparent"
echo "  PER-AGENT parked cost: checkpoint.img(state)=$(du -h $D/sb/checkpoint.img|cut -f1)  pages.img(delta)=$(du -h $D/sb/pages.img|cut -f1)  pages_meta=$(du -h $D/sb/pages_meta.img|cut -f1)"
} >> $H/resultC.txt
$R $KROOT delete --force seed 2>/dev/null; pkill -9 -f "runsc-sandbox.*root=/run/expC" 2>/dev/null; sleep 2
$R $KROOT run -bundle $B seed2 > $H/seed2C.log 2>&1 &
until grep -q "tick=" $H/seed2C.log 2>/dev/null; do sleep 0.05; done
sleep 8
$R $KROOT checkpoint --image-path=$D/full seed2 >/dev/null 2>&1
echo "  vs NORMAL checkpoint (no base): pages.img(full)=$(du -h $D/full/pages.img|cut -f1) + checkpoint.img=$(du -h $D/full/checkpoint.img|cut -f1)" >> $H/resultC.txt
echo "  => park N: shared-base = base_once + N*(state+delta); normal = N*full" >> $H/resultC.txt
$R $KROOT delete --force seed2 2>/dev/null; pkill -9 -f "runsc-sandbox.*root=/run/expC" 2>/dev/null; sleep 2

### PART 2: systrap latency (no /dev/kvm needed) ###
D2=$H/expCs; rm -rf $D2; mkdir -p $D2/ck
SROOT="--root /run/expCs --platform=systrap --ignore-cgroups --network=none"
cpu_of(){ local t=0 st rest; for pid in $(pgrep -f "runsc.*root=/run/expCs"); do st=$(cat /proc/$pid/stat 2>/dev/null); [ -z "$st" ]&&continue; rest=${st#*) }; set -- $rest; t=$((t+${12}+${13})); done; echo "scale=2;$t/$CLK"|bc; }
{ echo ""; echo "=== #2 SYSTRAP latency (platform-independent; no /dev/kvm) ==="; } >> $H/resultC.txt
t0=$(date +%s.%N)
$R $SROOT run -bundle $B scold > $H/scold.log 2>&1 &
until grep -q "tick=" $H/scold.log 2>/dev/null; do sleep 0.05; done
t1=$(date +%s.%N)
sleep 0.3; ccpu=$(cpu_of)
echo "SYSTRAP cold start (run->serving): wall=$(echo "$t1-$t0"|bc)s cpu=${ccpu}s" >> $H/resultC.txt
$R $SROOT checkpoint --image-path=$D2/ck scold >/dev/null 2>&1
$R $SROOT delete --force scold 2>/dev/null; pkill -9 -f "runsc-sandbox.*root=/run/expCs" 2>/dev/null; sleep 2
t0=$(date +%s.%N)
$R $SROOT restore --image-path=$D2/ck -bundle $B swarm > $H/swarm.log 2>&1 &
until grep -q "tick=" $H/swarm.log 2>/dev/null; do sleep 0.02; done
t1=$(date +%s.%N)
sleep 0.3; rcpu=$(cpu_of)
{
echo "SYSTRAP restore (restore->serving): wall=$(echo "$t1-$t0"|bc)s cpu=${rcpu}s"
echo "SYSTRAP restore correctness: resp=BAD=$(grep -c resp=BAD $H/swarm.log)"
echo "  => KVM was cold 11.4s/7.1cpu vs restore 0.6s; compare systrap above"
} >> $H/resultC.txt
$R $SROOT delete --force swarm 2>/dev/null; pkill -9 -f "runsc-sandbox.*root=/run/expCs" 2>/dev/null
echo "=== DONE ===" >> $H/resultC.txt
